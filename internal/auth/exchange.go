package auth

import (
	"crypto/rand"
	"errors"
	"fmt"
	"io"

	"github.com/goopg/goopg/internal/protocol"
)

// Exchange runs the wire-level authentication exchange dictated by a
// Decision. Callers (the server) supply the already-handshaked
// FrameReader/FrameWriter, the username/database from the
// StartupMessage, and a UserStore (or nil — see below).
//
// Return values:
//   - nil error: the connection is authenticated; the caller should
//     proceed to send the parameter-status block. AuthenticationOk has
//     been written *and flushed* before this function returns.
//   - ErrRejected: the policy or the credential rejected the
//     connection. The caller writes a FATAL ErrorResponse and closes.
//     AuthenticationOk has *not* been written.
//   - ErrMethodUnsupported: the matched method has no v0 implementation.
//     The caller emits a FATAL ErrorResponse explaining which method
//     and closes.
//   - ErrAuthExchange / ErrUnexpectedFrame: a wire-level failure during
//     the exchange. The caller logs and closes; no FATAL is needed
//     because the connection is already broken or the client misbehaved.
//
// A nil UserStore is acceptable when the decision is `trust` or a reject
// (those don't need credentials). Methods that need credentials with a
// nil store report ErrMethodUnsupported with a clear message rather
// than panicking.
func Exchange(d Decision, r *protocol.FrameReader, w *protocol.FrameWriter, store UserStore, user string) error {
	switch d.Method {
	case MethodTrust:
		if err := w.WriteAuthenticationOk(); err != nil {
			return ErrAuthExchange{Err: err}
		}
		if err := w.Flush(); err != nil {
			return ErrAuthExchange{Err: err}
		}
		return nil

	case MethodReject, MethodImplicitReject:
		return ErrRejected{Decision: d}

	case MethodPassword:
		return runCleartext(d, r, w, store, user)

	case MethodMD5:
		return runMD5(d, r, w, store, user)

	case MethodSCRAMSHA256:
		return runSCRAM(d, r, w, store, user)

	default:
		return ErrMethodUnsupported{Method: d.Method}
	}
}

// runCleartext implements the AuthenticationCleartextPassword exchange.
// We send 'R'/3, the client replies with a PasswordMessage carrying the
// password, and we compare it constant-time to the stored credential.
func runCleartext(d Decision, r *protocol.FrameReader, w *protocol.FrameWriter, store UserStore, user string) error {
	if store == nil {
		return ErrMethodUnsupported{Method: d.Method}
	}
	if err := w.WriteAuthenticationCleartextPassword(); err != nil {
		return ErrAuthExchange{Err: err}
	}
	if err := w.Flush(); err != nil {
		return ErrAuthExchange{Err: err}
	}

	given, err := readPasswordMessage(r)
	if err != nil {
		return err
	}

	cred, ok := store.Lookup(user)
	if !ok {
		return ErrUserUnknown{User: user}
	}
	if !cred.VerifyCleartext(user, given) {
		return ErrInvalidPassword{User: user}
	}

	if err := w.WriteAuthenticationOk(); err != nil {
		return ErrAuthExchange{Err: err}
	}
	if err := w.Flush(); err != nil {
		return ErrAuthExchange{Err: err}
	}
	return nil
}

// runMD5 implements the AuthenticationMD5Password exchange. We send
// 'R'/5/salt(4), the client replies with PasswordMessage of the form
// "md5"+md5_hex(md5_hex(password+username)+salt), and we reproduce the
// expected response from the stored credential and compare.
func runMD5(d Decision, r *protocol.FrameReader, w *protocol.FrameWriter, store UserStore, user string) error {
	if store == nil {
		return ErrMethodUnsupported{Method: d.Method}
	}
	var salt [4]byte
	if _, err := rand.Read(salt[:]); err != nil {
		return ErrAuthExchange{Err: fmt.Errorf("md5 salt: %w", err)}
	}
	if err := w.WriteAuthenticationMD5Password(salt); err != nil {
		return ErrAuthExchange{Err: err}
	}
	if err := w.Flush(); err != nil {
		return ErrAuthExchange{Err: err}
	}

	given, err := readPasswordMessage(r)
	if err != nil {
		return err
	}

	cred, ok := store.Lookup(user)
	if !ok {
		return ErrUserUnknown{User: user}
	}
	if !cred.VerifyMD5Challenge(user, salt, given) {
		return ErrInvalidPassword{User: user}
	}

	if err := w.WriteAuthenticationOk(); err != nil {
		return ErrAuthExchange{Err: err}
	}
	if err := w.Flush(); err != nil {
		return ErrAuthExchange{Err: err}
	}
	return nil
}

// runSCRAM implements the SASL/SCRAM-SHA-256 server side. Wire shape:
//
//	server -> 'R' AuthenticationSASL [SCRAM-SHA-256\0\0]
//	client -> 'p' SASLInitialResponse: mechanism\0 + int32 len + client-first-message
//	server -> 'R' AuthenticationSASLContinue [server-first-message]
//	client -> 'p' SASLResponse: client-final-message
//	server -> 'R' AuthenticationSASLFinal [server-final-message]
//	server -> 'R' AuthenticationOk
//
// See postgres/src/backend/libpq/auth.c:CheckSASLAuth and
// auth-scram.c:scram_init/scram_exchange for the upstream reference.
func runSCRAM(d Decision, r *protocol.FrameReader, w *protocol.FrameWriter, store UserStore, user string) error {
	if store == nil {
		return ErrMethodUnsupported{Method: d.Method}
	}

	if err := w.WriteAuthenticationSASL([]string{scramMechanism}); err != nil {
		return ErrAuthExchange{Err: err}
	}
	if err := w.Flush(); err != nil {
		return ErrAuthExchange{Err: err}
	}

	// SASLInitialResponse: mechanism\0 + int32 dataLen + data
	mech, clientFirst, err := readSASLInitial(r)
	if err != nil {
		return err
	}
	if mech != scramMechanism {
		return ErrAuthExchange{Err: fmt.Errorf("client selected unsupported SASL mechanism %q", mech)}
	}

	// Lookup credential. Unknown user -> doomed exchange (mock secret).
	var secret *SCRAMSecret
	if cred, ok := store.Lookup(user); ok {
		switch cred.Type {
		case PasswordSCRAMSHA256:
			s, perr := ParseSCRAMSecret(cred.Secret)
			if perr == nil {
				secret = s
			}
		case PasswordPlaintext:
			s, derr := NewSCRAMSecret(cred.Secret)
			if derr == nil {
				secret = s
			}
		}
		// PasswordMD5 cannot satisfy SCRAM (no plaintext available).
		// secret stays nil -> doomed exchange.
	}

	server, err := NewSCRAMServer(user, secret)
	if err != nil {
		return ErrAuthExchange{Err: err}
	}

	serverFirst, _, err := server.Step(clientFirst)
	if err != nil {
		return ErrAuthExchange{Err: err}
	}
	if err := w.WriteAuthenticationSASLContinue(serverFirst); err != nil {
		return ErrAuthExchange{Err: err}
	}
	if err := w.Flush(); err != nil {
		return ErrAuthExchange{Err: err}
	}

	clientFinal, err := readSASLResponse(r)
	if err != nil {
		return err
	}
	serverFinal, done, err := server.Step(clientFinal)
	if err != nil {
		// Bad proof / doomed user -> ErrInvalidPassword. Anything else
		// is an exchange-level malformed-message error.
		var bad ErrInvalidPassword
		if errors.As(err, &bad) {
			return bad
		}
		return ErrAuthExchange{Err: err}
	}
	if !done {
		return ErrAuthExchange{Err: errors.New("scram: unexpected continuation after final")}
	}
	if err := w.WriteAuthenticationSASLFinal(serverFinal); err != nil {
		return ErrAuthExchange{Err: err}
	}
	if err := w.WriteAuthenticationOk(); err != nil {
		return ErrAuthExchange{Err: err}
	}
	if err := w.Flush(); err != nil {
		return ErrAuthExchange{Err: err}
	}
	if secret == nil {
		// We completed the exchange (timing parity) but there was no
		// real credential — fall back to ErrInvalidPassword so the
		// caller emits FATAL/28000. server.Step would normally have
		// produced this above, but we belt-and-brace here in case the
		// proof happened to "verify" against the mock (vanishingly
		// unlikely but cheap to defend against).
		return ErrInvalidPassword{User: user}
	}
	return nil
}

// readSASLInitial decodes a SASLInitialResponse frame into (mechanism,
// initial-response-bytes). The initial response is 4-byte length followed
// by raw bytes; -1 indicates the absence of an initial response, which we
// treat as zero-length (consistent with how upstream's read_client_first_message
// handles it).
func readSASLInitial(r *protocol.FrameReader) (string, []byte, error) {
	f, err := r.ReadFrame()
	if err != nil {
		return "", nil, ErrAuthExchange{Err: err}
	}
	if f.Type != protocol.MsgPasswordMessage {
		return "", nil, ErrUnexpectedFrame
	}
	// Mechanism\0 + int32 length + data
	idx := -1
	for i, b := range f.Payload {
		if b == 0 {
			idx = i
			break
		}
	}
	if idx < 0 {
		return "", nil, ErrAuthExchange{Err: errors.New("SASLInitialResponse: missing mechanism NUL")}
	}
	mech := string(f.Payload[:idx])
	rest := f.Payload[idx+1:]
	if len(rest) < 4 {
		return "", nil, ErrAuthExchange{Err: errors.New("SASLInitialResponse: missing length")}
	}
	dataLen := int32(uint32(rest[0])<<24 | uint32(rest[1])<<16 | uint32(rest[2])<<8 | uint32(rest[3]))
	rest = rest[4:]
	if dataLen < 0 {
		return mech, nil, nil
	}
	if int(dataLen) > len(rest) {
		return "", nil, ErrAuthExchange{Err: fmt.Errorf("SASLInitialResponse: dataLen %d exceeds payload %d", dataLen, len(rest))}
	}
	data := make([]byte, dataLen)
	copy(data, rest[:dataLen])
	return mech, data, nil
}

// readSASLResponse decodes a continuation SASLResponse frame whose payload
// is just the mechanism-specific bytes.
func readSASLResponse(r *protocol.FrameReader) ([]byte, error) {
	f, err := r.ReadFrame()
	if err != nil {
		return nil, ErrAuthExchange{Err: err}
	}
	if f.Type != protocol.MsgPasswordMessage {
		return nil, ErrUnexpectedFrame
	}
	out := make([]byte, len(f.Payload))
	copy(out, f.Payload)
	return out, nil
}

func readPasswordMessage(r *protocol.FrameReader) (string, error) {
	f, err := r.ReadFrame()
	if err != nil {
		if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
			return "", ErrAuthExchange{Err: err}
		}
		return "", ErrAuthExchange{Err: err}
	}
	if f.Type != protocol.MsgPasswordMessage {
		return "", ErrUnexpectedFrame
	}
	// Payload is "<password>\0".
	for i, b := range f.Payload {
		if b == 0 {
			return string(f.Payload[:i]), nil
		}
	}
	return "", ErrAuthExchange{Err: errors.New("PasswordMessage missing NUL terminator")}
}
