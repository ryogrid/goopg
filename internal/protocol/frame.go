package protocol

import (
	"bufio"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
)

// Frame is one PostgreSQL post-startup message: a 1-byte type tag and a
// payload (which excludes the 4-byte length header in the wire form).
//
// The Payload slice is borrowed from the FrameReader's internal buffer and
// is only valid until the next ReadFrame call. Callers that need the bytes
// to outlive the next read must copy.
type Frame struct {
	Type    byte
	Payload []byte
}

// FrameReader reads length-prefixed PostgreSQL messages from an io.Reader.
//
// Use ReadStartupPacket exactly once at the start of a connection (the
// startup packet has no leading type byte). Use ReadFrame for every
// subsequent message.
type FrameReader struct {
	r        *bufio.Reader
	buf      []byte // grows on demand, capped by MaxRegularMessageLength
	maxStart int
	maxRegul int

	// OnBeforeRead / OnAfterRead are optional hooks called before and
	// after every ReadFrame / ReadStartupPacket. The server sets these
	// from serveConn so the activity package can record ClientRead
	// wait events (Stage B wait-event taxonomy).
	OnBeforeRead func()
	OnAfterRead  func()
}

// NewFrameReader wraps r with the v0 message-size limits. Tests can pass a
// smaller maxRegular to exercise overflow handling.
func NewFrameReader(r io.Reader) *FrameReader {
	return &FrameReader{
		r:        bufio.NewReader(r),
		maxStart: MaxStartupPacketLength,
		maxRegul: MaxRegularMessageLength,
	}
}

// ReadStartupPacket reads the very first message on a connection. It returns
// the 4-byte version-or-special-code value and the remaining payload (all
// bytes after the 4-byte version, NUL-terminated key/value list for a
// regular startup, or special-code-specific bytes otherwise).
//
// On EOF before any byte has been read, it returns (0, nil, io.EOF) so the
// caller can distinguish "client closed without sending anything" (a common
// no-op probe) from "client sent a partial packet".
func (fr *FrameReader) ReadStartupPacket() (version uint32, payload []byte, err error) {
	if fr.OnBeforeRead != nil {
		fr.OnBeforeRead()
	}
	defer func() {
		if fr.OnAfterRead != nil {
			fr.OnAfterRead()
		}
	}()
	var lenBuf [4]byte
	n, err := io.ReadFull(fr.r, lenBuf[:1])
	if err != nil {
		if err == io.EOF || err == io.ErrUnexpectedEOF {
			return 0, nil, io.EOF
		}
		return 0, nil, fmt.Errorf("read startup length byte: %w", err)
	}
	if n == 0 {
		return 0, nil, io.EOF
	}
	if _, err := io.ReadFull(fr.r, lenBuf[1:]); err != nil {
		return 0, nil, fmt.Errorf("incomplete startup length: %w", err)
	}
	total := binary.BigEndian.Uint32(lenBuf[:])
	if total < 8 || int(total) > fr.maxStart {
		return 0, nil, fmt.Errorf("invalid startup packet length %d", total)
	}
	body := make([]byte, int(total)-4)
	if _, err := io.ReadFull(fr.r, body); err != nil {
		return 0, nil, fmt.Errorf("incomplete startup packet body: %w", err)
	}
	version = binary.BigEndian.Uint32(body[:4])
	return version, body[4:], nil
}

// ReadFrame reads one post-startup message. The returned Frame.Payload aliases
// an internal buffer reused on the next call; copy if you need to retain it.
func (fr *FrameReader) ReadFrame() (Frame, error) {
	if fr.OnBeforeRead != nil {
		fr.OnBeforeRead()
	}
	defer func() {
		if fr.OnAfterRead != nil {
			fr.OnAfterRead()
		}
	}()
	var hdr [5]byte
	if _, err := io.ReadFull(fr.r, hdr[:]); err != nil {
		if err == io.ErrUnexpectedEOF {
			return Frame{}, io.ErrUnexpectedEOF
		}
		return Frame{}, err
	}
	total := binary.BigEndian.Uint32(hdr[1:5])
	if total < 4 {
		return Frame{}, fmt.Errorf("frame %q: length %d below minimum", hdr[0], total)
	}
	payloadLen := int(total) - 4
	if payloadLen > fr.maxRegul {
		// Drain the oversized payload so the connection stream stays
		// synchronised — allows the caller to send a proper error response
		// and continue instead of dropping the connection silently.
		if _, drainErr := io.CopyN(io.Discard, fr.r, int64(payloadLen)); drainErr != nil {
			return Frame{}, fmt.Errorf("frame %q: payload %d exceeds limit %d, drain failed: %w", hdr[0], payloadLen, fr.maxRegul, drainErr)
		}
		return Frame{}, fmt.Errorf("%w: frame %q payload %d bytes", ErrFrameTooLarge, hdr[0], payloadLen)
	}
	if cap(fr.buf) < payloadLen {
		fr.buf = make([]byte, payloadLen)
	} else {
		fr.buf = fr.buf[:payloadLen]
	}
	if payloadLen > 0 {
		if _, err := io.ReadFull(fr.r, fr.buf); err != nil {
			return Frame{}, fmt.Errorf("frame %q: %w", hdr[0], err)
		}
	}
	return Frame{Type: hdr[0], Payload: fr.buf}, nil
}

// FrameWriter emits PostgreSQL backend messages to an io.Writer.
//
// FrameWriter buffers writes; call Flush to commit them. Concurrent use is
// not supported — pair one FrameWriter with one connection-handler goroutine.
type FrameWriter struct {
	w   *bufio.Writer
	hdr [5]byte

	// OnBeforeWrite / OnAfterWrite are optional hooks called before and
	// after every WriteFrame / WriteRaw / Flush. The server sets these
	// from serveConn so the activity package can record ClientWrite
	// wait events (Stage B wait-event taxonomy).
	OnBeforeWrite func()
	OnAfterWrite  func()
}

// NewFrameWriter wraps w with a buffered writer sized for typical message
// volumes during startup (tens of small messages back-to-back).
func NewFrameWriter(w io.Writer) *FrameWriter {
	return &FrameWriter{w: bufio.NewWriter(w)}
}

// WriteFrame emits a complete message: type byte, 4-byte big-endian length
// (payload length + 4), then payload. Returns the first error encountered;
// callers typically Flush() once after a logical batch (e.g. the full
// startup reply).
func (fw *FrameWriter) WriteFrame(typ byte, payload []byte) error {
	if fw.OnBeforeWrite != nil {
		fw.OnBeforeWrite()
	}
	defer func() {
		if fw.OnAfterWrite != nil {
			fw.OnAfterWrite()
		}
	}()
	if len(payload) > MaxRegularMessageLength {
		return fmt.Errorf("WriteFrame %q: payload %d exceeds %d", typ, len(payload), MaxRegularMessageLength)
	}
	fw.hdr[0] = typ
	binary.BigEndian.PutUint32(fw.hdr[1:5], uint32(len(payload)+4))
	if _, err := fw.w.Write(fw.hdr[:]); err != nil {
		return err
	}
	if len(payload) > 0 {
		if _, err := fw.w.Write(payload); err != nil {
			return err
		}
	}
	return nil
}

// WriteRaw emits raw bytes (no framing) — used for SSLRequest/GSSENCRequest
// single-byte responses ('N' / 'S'), where the byte is sent before any
// framed messages exist.
func (fw *FrameWriter) WriteRaw(b []byte) error {
	if fw.OnBeforeWrite != nil {
		fw.OnBeforeWrite()
	}
	_, err := fw.w.Write(b)
	if fw.OnAfterWrite != nil {
		fw.OnAfterWrite()
	}
	return err
}

// Flush flushes the underlying buffered writer.
func (fw *FrameWriter) Flush() error {
	if fw.OnBeforeWrite != nil {
		fw.OnBeforeWrite()
	}
	err := fw.w.Flush()
	if fw.OnAfterWrite != nil {
		fw.OnAfterWrite()
	}
	return err
}

// ParseStartupParameters parses the NUL-terminated key/value pairs that
// follow the 4-byte version of a regular startup packet. It returns a map
// of parameters in insertion order using a slice (preserving order matters
// for round-tripping `application_name` and similar). Duplicate keys keep
// the first value (matches upstream behaviour).
//
// The buffer is expected to be the slice returned by ReadStartupPacket
// (i.e. without the 4-byte version prefix). It must end in a NUL byte
// terminating the final empty key.
func ParseStartupParameters(buf []byte) (map[string]string, error) {
	params := map[string]string{}
	if len(buf) == 0 {
		return nil, errors.New("startup parameters: empty buffer")
	}
	i := 0
	for i < len(buf) {
		key, n, err := readCString(buf[i:])
		if err != nil {
			return nil, fmt.Errorf("startup parameter key: %w", err)
		}
		i += n
		if key == "" {
			return params, nil
		}
		if i >= len(buf) {
			return nil, errors.New("startup parameters: missing value")
		}
		val, n, err := readCString(buf[i:])
		if err != nil {
			return nil, fmt.Errorf("startup parameter value for %q: %w", key, err)
		}
		i += n
		if _, exists := params[key]; !exists {
			params[key] = val
		}
	}
	return nil, errors.New("startup parameters: missing terminating empty key")
}

// readCString parses one NUL-terminated string and returns the string, the
// number of bytes consumed (including the NUL), and an error if no NUL was
// found.
func readCString(buf []byte) (string, int, error) {
	for i, b := range buf {
		if b == 0 {
			return string(buf[:i]), i + 1, nil
		}
	}
	return "", 0, errors.New("unterminated C string")
}
