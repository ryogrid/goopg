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
