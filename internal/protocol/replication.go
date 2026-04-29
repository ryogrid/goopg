// Streaming-replication wire encoders/decoders.
//
// The outer frame for replication is `MsgCopyBothResponse` ('W') from
// the backend, then bidirectional `MsgCopyData` ('d') frames carry
// inner payloads that follow PostgreSQL's "walprotocol" framing
// (postgres/src/include/replication/walprotocol.h):
//
//   server → client (inside CopyData):
//     'w' | startLSN(8) | endLSN(8) | sendTime(8) | wal_bytes...
//     'k' | walEnd(8)   | sendTime(8) | replyRequested(1)
//
//   client → server (inside CopyData):
//     'r' | writeLSN(8) | flushLSN(8) | applyLSN(8) | sendTime(8) | replyRequested(1)
//     'h' | applyLSN(8) | applyTime(8) | catalogXmin(4) | epoch(4)   (deferred — v0 doesn't emit this)
//
// `sendTime` is microseconds since 2000-01-01 00:00:00 UTC, matching
// upstream's `TimestampTz` epoch (postgres/src/include/datatype/timestamp.h).
//
// See docs/design/0005-0001-streaming-replication-architecture.md.
package protocol

import (
	"encoding/binary"
	"fmt"
	"time"
)

// Replication inner-message type bytes (the first byte of a CopyData
// payload during streaming replication). Names mirror upstream's
// `WalSndMsgType` / `WalRcvMsgType` constants in
// postgres/src/include/replication/walprotocol.h.
const (
	ReplMsgWALData         byte = 'w'
	ReplMsgKeepalive       byte = 'k'
	ReplMsgStandbyStatus   byte = 'r'
	ReplMsgHotStandbyFeedback byte = 'h'
)

// pgEpoch is the upstream TimestampTz epoch: midnight 2000-01-01 UTC.
// Walprotocol timestamps are microseconds since this instant, big-endian.
var pgEpoch = time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC)

// PgTimestampMicros converts a Go time.Time into upstream's TimestampTz
// (microseconds since 2000-01-01 UTC). Negative results are valid for
// pre-epoch instants but the streaming-replication path never produces
// them (sendTime is always "now").
func PgTimestampMicros(t time.Time) int64 {
	return t.Sub(pgEpoch).Microseconds()
}

// PgTimestampToTime is the inverse of PgTimestampMicros.
func PgTimestampToTime(micros int64) time.Time {
	return pgEpoch.Add(time.Duration(micros) * time.Microsecond)
}

// EncodeWALData builds the body of a 'w' (WAL data) CopyData payload.
// Caller wraps the returned bytes with WriteCopyData.
//
//	'w' | startLSN(8) | endLSN(8) | sendTime(8) | walBytes...
//
// startLSN is the LSN of the first byte in walBytes; endLSN is the
// (exclusive) LSN of the byte one past the last in walBytes; sendTime
// is when this frame was emitted.
func EncodeWALData(startLSN, endLSN uint64, sendTime time.Time, walBytes []byte) []byte {
	out := make([]byte, 0, 1+8+8+8+len(walBytes))
	out = append(out, ReplMsgWALData)
	out = appendUint64(out, startLSN)
	out = appendUint64(out, endLSN)
	out = appendUint64(out, uint64(PgTimestampMicros(sendTime)))
	out = append(out, walBytes...)
	return out
}

// EncodeKeepalive builds a 'k' (keepalive) CopyData payload.
//
//	'k' | walEnd(8) | sendTime(8) | replyRequested(1)
//
// walEnd is the latest WAL byte position the primary has produced
// (whether or not it has been streamed to this standby). When
// replyRequested is true, the standby must respond with a status
// update at its earliest convenience.
func EncodeKeepalive(walEnd uint64, sendTime time.Time, replyRequested bool) []byte {
	out := make([]byte, 0, 1+8+8+1)
	out = append(out, ReplMsgKeepalive)
	out = appendUint64(out, walEnd)
	out = appendUint64(out, uint64(PgTimestampMicros(sendTime)))
	if replyRequested {
		out = append(out, 1)
	} else {
		out = append(out, 0)
	}
	return out
}

// EncodeStandbyStatusUpdate builds an 'r' (status update) CopyData
// payload. Sent by the standby (client side) — exposed here so a
// goopg-side walreceiver can produce it without re-deriving the
// layout.
//
//	'r' | writeLSN(8) | flushLSN(8) | applyLSN(8) | sendTime(8) | replyRequested(1)
func EncodeStandbyStatusUpdate(writeLSN, flushLSN, applyLSN uint64, sendTime time.Time, replyRequested bool) []byte {
	out := make([]byte, 0, 1+8+8+8+8+1)
	out = append(out, ReplMsgStandbyStatus)
	out = appendUint64(out, writeLSN)
	out = appendUint64(out, flushLSN)
	out = appendUint64(out, applyLSN)
	out = appendUint64(out, uint64(PgTimestampMicros(sendTime)))
	if replyRequested {
		out = append(out, 1)
	} else {
		out = append(out, 0)
	}
	return out
}

// WALDataMessage is the parsed shape of a 'w' (WAL data) CopyData
// payload. WALBytes references the original payload buffer — copy
// before holding past the next FrameReader call.
type WALDataMessage struct {
	StartLSN uint64
	EndLSN   uint64
	SendTime time.Time
	WALBytes []byte
}

// KeepaliveMessage is the parsed shape of a 'k' (keepalive) payload.
type KeepaliveMessage struct {
	WALEnd         uint64
	SendTime       time.Time
	ReplyRequested bool
}

// StandbyStatusUpdate is the parsed shape of an 'r' (status update)
// payload. The primary uses ApplyLSN / FlushLSN to advance the
// confirmed-flush position on the corresponding replication slot.
type StandbyStatusUpdate struct {
	WriteLSN       uint64
	FlushLSN       uint64
	ApplyLSN       uint64
	SendTime       time.Time
	ReplyRequested bool
}

// DecodeReplicationMessage classifies a CopyData payload by its first
// byte and decodes into the matching struct. Returns one of
// (*WALDataMessage, *KeepaliveMessage, *StandbyStatusUpdate) plus the
// inner-message type byte; nil + 0 + non-nil error on truncation or
// unknown type. The caller (server walsender, client walreceiver)
// dispatches on the returned concrete type.
func DecodeReplicationMessage(payload []byte) (any, byte, error) {
	if len(payload) < 1 {
		return nil, 0, fmt.Errorf("replication message: empty payload")
	}
	switch payload[0] {
	case ReplMsgWALData:
		m, err := decodeWALData(payload)
		if err != nil {
			return nil, 0, err
		}
		return m, ReplMsgWALData, nil
	case ReplMsgKeepalive:
		m, err := decodeKeepalive(payload)
		if err != nil {
			return nil, 0, err
		}
		return m, ReplMsgKeepalive, nil
	case ReplMsgStandbyStatus:
		m, err := decodeStandbyStatus(payload)
		if err != nil {
			return nil, 0, err
		}
		return m, ReplMsgStandbyStatus, nil
	default:
		return nil, 0, fmt.Errorf("replication message: unknown inner type %q", payload[0])
	}
}

func decodeWALData(payload []byte) (*WALDataMessage, error) {
	// 1 (type) + 24 (header) ≤ len(payload). WALBytes may be empty.
	if len(payload) < 1+24 {
		return nil, fmt.Errorf("WAL-data message: payload too short (%d < 25)", len(payload))
	}
	body := payload[1:]
	startLSN := binary.BigEndian.Uint64(body[0:8])
	endLSN := binary.BigEndian.Uint64(body[8:16])
	sendMicros := int64(binary.BigEndian.Uint64(body[16:24]))
	wal := body[24:]
	return &WALDataMessage{
		StartLSN: startLSN,
		EndLSN:   endLSN,
		SendTime: PgTimestampToTime(sendMicros),
		WALBytes: wal,
	}, nil
}

func decodeKeepalive(payload []byte) (*KeepaliveMessage, error) {
	if len(payload) != 1+8+8+1 {
		return nil, fmt.Errorf("keepalive: expected 18 bytes, got %d", len(payload))
	}
	body := payload[1:]
	walEnd := binary.BigEndian.Uint64(body[0:8])
	sendMicros := int64(binary.BigEndian.Uint64(body[8:16]))
	return &KeepaliveMessage{
		WALEnd:         walEnd,
		SendTime:       PgTimestampToTime(sendMicros),
		ReplyRequested: body[16] != 0,
	}, nil
}

func decodeStandbyStatus(payload []byte) (*StandbyStatusUpdate, error) {
	if len(payload) != 1+8+8+8+8+1 {
		return nil, fmt.Errorf("standby status: expected 34 bytes, got %d", len(payload))
	}
	body := payload[1:]
	writeLSN := binary.BigEndian.Uint64(body[0:8])
	flushLSN := binary.BigEndian.Uint64(body[8:16])
	applyLSN := binary.BigEndian.Uint64(body[16:24])
	sendMicros := int64(binary.BigEndian.Uint64(body[24:32]))
	return &StandbyStatusUpdate{
		WriteLSN:       writeLSN,
		FlushLSN:       flushLSN,
		ApplyLSN:       applyLSN,
		SendTime:       PgTimestampToTime(sendMicros),
		ReplyRequested: body[32] != 0,
	}, nil
}

// WriteCopyBothResponse emits a backend 'W' (CopyBothResponse) frame.
// Layout matches CopyInResponse / CopyOutResponse: int8 overall-format
// + int16 ncolumns + int16 format[*]. Streaming replication uses
// overall-format = 0 (text) and an empty column list — it's not a
// row stream, just the entry into bidirectional CopyData mode.
func (fw *FrameWriter) WriteCopyBothResponse(overallFormat byte, columnFormats []uint16) error {
	payload := make([]byte, 0, 1+2+2*len(columnFormats))
	payload = append(payload, overallFormat)
	payload = appendUint16(payload, uint16(len(columnFormats)))
	for _, f := range columnFormats {
		payload = appendUint16(payload, f)
	}
	return fw.WriteFrame(MsgCopyBothResponse, payload)
}

// appendUint64 mirrors the existing appendUint16 helper in messages.go.
// Kept package-local so replication-specific encoders compose without
// pulling encoding/binary into every call site.
func appendUint64(buf []byte, v uint64) []byte {
	var b [8]byte
	binary.BigEndian.PutUint64(b[:], v)
	return append(buf, b[:]...)
}
