package auth

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"strconv"
	"strings"
)

// SCRAM-SHA-256 implementation per RFC 5802 + RFC 7677, with the
// PostgreSQL-specific framing rules (channel binding "n,," — no binding
// — until TLS lands; then "y,," for "I'd use binding but server didn't
// advertise"; "p=tls-server-end-point" once we expose tls-server-end-point).
//
// Upstream reference:
//   - postgres/src/backend/libpq/auth-scram.c (server-side state machine)
//   - postgres/src/common/scram-common.c     (key derivation primitives)
//   - postgres/src/include/common/scram-common.h (constants)

const (
	// scramMechanism is the only mechanism goopg advertises today.
	// SCRAM-SHA-256-PLUS is added once we offer tls-server-end-point
	// channel binding (post-TLS).
	scramMechanism = "SCRAM-SHA-256"

	// scramKeyLen is SCRAM_SHA_256_KEY_LEN = SHA-256 digest length.
	// (postgres/src/include/common/scram-common.h:24)
	scramKeyLen = 32

	// scramRawNonceLen is SCRAM_RAW_NONCE_LEN.
	// (postgres/src/include/common/scram-common.h:37)
	scramRawNonceLen = 18

	// scramDefaultSaltLen is SCRAM_DEFAULT_SALT_LEN.
	// (postgres/src/include/common/scram-common.h:44)
	scramDefaultSaltLen = 16

	// scramDefaultIterations is SCRAM_SHA_256_DEFAULT_ITERATIONS.
	// (postgres/src/include/common/scram-common.h:50)
	scramDefaultIterations = 4096
)

// SCRAMSecret is the parsed form of the upstream rolpassword
//
//	SCRAM-SHA-256$<iterations>:<salt-b64>$<StoredKey-b64>:<ServerKey-b64>
//
// (postgres/src/backend/libpq/auth-scram.c:600 parse_scram_secret).
type SCRAMSecret struct {
	Iterations int
	Salt       []byte // base64-decoded salt bytes
	StoredKey  []byte // 32 bytes
	ServerKey  []byte // 32 bytes
}

// EncodedSalt returns the base64-encoded salt as it appears on the wire
// in the server-first-message.
func (s *SCRAMSecret) EncodedSalt() string {
	return base64.StdEncoding.EncodeToString(s.Salt)
}

// NewSCRAMSecret derives a SCRAMSecret from a cleartext password using
// PostgreSQL's default salt length and iteration count. Intended for
// development and tests; production deployments will store the
// already-derived form.
func NewSCRAMSecret(password string) (*SCRAMSecret, error) {
	return NewSCRAMSecretWithIterations(password, scramDefaultIterations)
}

// NewSCRAMSecretWithIterations derives a SCRAMSecret using an explicit
// iteration count, mirroring upstream's CreateRole/encrypt_password path
// which reads the scram_iterations GUC (postgres/src/backend/commands/
// user.c, postgres/src/common/scram-common.c scram_build_secret) instead
// of always using SCRAM_SHA_256_DEFAULT_ITERATIONS. A non-positive count
// falls back to scramDefaultIterations.
func NewSCRAMSecretWithIterations(password string, iterations int) (*SCRAMSecret, error) {
	if iterations <= 0 {
		iterations = scramDefaultIterations
	}
	salt := make([]byte, scramDefaultSaltLen)
	if _, err := rand.Read(salt); err != nil {
		return nil, fmt.Errorf("scram salt: %w", err)
	}
	return scramBuildSecret(saslPrepOrOriginal(password), salt, iterations), nil
}

// ParseSCRAMSecret parses an upstream-format rolpassword string. The
// shape is documented in postgres/src/backend/libpq/auth-scram.c:619.
func ParseSCRAMSecret(secret string) (*SCRAMSecret, error) {
	scheme, rest, ok := strings.Cut(secret, "$")
	if !ok || scheme != "SCRAM-SHA-256" {
		return nil, errors.New("scram: not a SCRAM-SHA-256 secret")
	}
	iterAndSalt, keys, ok := strings.Cut(rest, "$")
	if !ok {
		return nil, errors.New("scram: missing keys section")
	}
	iterStr, saltStr, ok := strings.Cut(iterAndSalt, ":")
	if !ok {
		return nil, errors.New("scram: missing salt")
	}
	storedStr, serverStr, ok := strings.Cut(keys, ":")
	if !ok {
		return nil, errors.New("scram: missing ServerKey")
	}
	iter, err := strconv.Atoi(iterStr)
	if err != nil || iter <= 0 {
		return nil, fmt.Errorf("scram: bad iteration count %q", iterStr)
	}
	salt, err := base64.StdEncoding.DecodeString(saltStr)
	if err != nil {
		return nil, fmt.Errorf("scram: bad salt b64: %w", err)
	}
	stored, err := base64.StdEncoding.DecodeString(storedStr)
	if err != nil || len(stored) != scramKeyLen {
		return nil, fmt.Errorf("scram: bad StoredKey b64 (len=%d)", len(stored))
	}
	server, err := base64.StdEncoding.DecodeString(serverStr)
	if err != nil || len(server) != scramKeyLen {
		return nil, fmt.Errorf("scram: bad ServerKey b64 (len=%d)", len(server))
	}
	return &SCRAMSecret{
		Iterations: iter,
		Salt:       salt,
		StoredKey:  stored,
		ServerKey:  server,
	}, nil
}

// String emits the upstream rolpassword form.
func (s *SCRAMSecret) String() string {
	return fmt.Sprintf("SCRAM-SHA-256$%d:%s$%s:%s",
		s.Iterations,
		base64.StdEncoding.EncodeToString(s.Salt),
		base64.StdEncoding.EncodeToString(s.StoredKey),
		base64.StdEncoding.EncodeToString(s.ServerKey))
}

// NewSCRAMCredential builds a SCRAM-SHA-256 Credential from a cleartext
// password by deriving a fresh secret. Convenience for tests and small
// setups.
func NewSCRAMCredential(password string) (Credential, error) {
	s, err := NewSCRAMSecret(password)
	if err != nil {
		return Credential{}, err
	}
	return Credential{Type: PasswordSCRAMSHA256, Secret: s.String()}, nil
}

// scramBuildSecret runs the SCRAM key derivation:
//
//	SaltedPassword := PBKDF2-HMAC-SHA-256(password, salt, iter, 32)
//	ClientKey      := HMAC(SaltedPassword, "Client Key")
//	StoredKey      := H(ClientKey)
//	ServerKey      := HMAC(SaltedPassword, "Server Key")
//
// See postgres/src/common/scram-common.c:scram_build_secret.
func scramBuildSecret(password string, salt []byte, iter int) *SCRAMSecret {
	salted := pbkdf2HMACSHA256([]byte(password), salt, iter, scramKeyLen)
	clientKey := hmacSHA256(salted, []byte("Client Key"))
	storedKey := sha256Sum(clientKey)
	serverKey := hmacSHA256(salted, []byte("Server Key"))
	return &SCRAMSecret{
		Iterations: iter,
		Salt:       append([]byte(nil), salt...),
		StoredKey:  storedKey,
		ServerKey:  serverKey,
	}
}

// VerifySCRAMSecretFromPassword recomputes the StoredKey from a
// candidate password and compares it (constant-time) to s.StoredKey.
// Used by the cleartext path against a SCRAM-shadowed credential —
// matches scram_verify_plain_password in upstream.
func (s *SCRAMSecret) VerifySCRAMSecretFromPassword(password string) bool {
	c := scramBuildSecret(saslPrepOrOriginal(password), s.Salt, s.Iterations)
	return subtle.ConstantTimeCompare(c.StoredKey, s.StoredKey) == 1
}

// SCRAMServer holds the per-connection state of a SCRAM-SHA-256 server
// exchange. Use NewSCRAMServer + Step() in the order:
//
//	srv.Step(client_first_message)  -> server_first_message,  done=false
//	srv.Step(client_final_message)  -> server_final_message,  done=true (success)
//
// On a verification failure, Step returns ErrInvalidPassword (so the
// caller produces the same FATAL ErrorResponse it would for a wrong
// cleartext password — see RFC 5802 §5.1).
//
// The server runs in "doomed" mode when the user is unknown: it still
// performs all the cryptographic work (timing parity, mirroring upstream
// auth-scram.c:442), but the final proof check fails. We model this by
// retaining a fake SCRAMSecret produced from a stable per-connection
// pseudorandom seed.
type SCRAMServer struct {
	username string
	secret   *SCRAMSecret
	doomed   bool

	clientNonce string
	serverNonce string

	// authMessage = client-first-bare + "," + server-first + "," + client-final-without-proof
	// (RFC 5802 §3, used for verifying the client proof and computing the
	// server signature.)
	authMessage string

	state scramState
}

type scramState int

const (
	scramStateInit scramState = iota
	scramStateAwaitingFinal
	scramStateDone
)

// NewSCRAMServer constructs the server state. secret may be nil — that
// case is the "doomed" mode for unknown users; we fabricate a plausible
// secret so the exchange runs to completion and can fail at the proof
// check, matching upstream's posture.
func NewSCRAMServer(username string, secret *SCRAMSecret) (*SCRAMServer, error) {
	s := &SCRAMServer{username: username}
	nonce := make([]byte, scramRawNonceLen)
	if _, err := rand.Read(nonce); err != nil {
		return nil, fmt.Errorf("scram nonce: %w", err)
	}
	s.serverNonce = base64.StdEncoding.EncodeToString(nonce)
	if secret == nil {
		// Mock secret: deterministic-looking but unrelated to any real
		// password. Operators see "auth failed" on the wire either way.
		mockSalt := make([]byte, scramDefaultSaltLen)
		_, _ = rand.Read(mockSalt)
		s.secret = scramBuildSecret("\x00"+username+"\x00mock", mockSalt, scramDefaultIterations)
		s.doomed = true
	} else {
		s.secret = secret
	}
	return s, nil
}

// Step drives one half of the SCRAM exchange. Returns the bytes to send
// to the client, a "done" flag (true after a successful final message),
// and an error.
//
// On the first call, input is the client-first-message and the function
// returns the server-first-message. On the second call, input is the
// client-final-message and the function returns the server-final-message
// (or an error if verification failed).
func (s *SCRAMServer) Step(input []byte) ([]byte, bool, error) {
	switch s.state {
	case scramStateInit:
		out, err := s.handleClientFirst(input)
		if err != nil {
			return nil, false, err
		}
		s.state = scramStateAwaitingFinal
		return out, false, nil
	case scramStateAwaitingFinal:
		out, err := s.handleClientFinal(input)
		if err != nil {
			return nil, false, err
		}
		s.state = scramStateDone
		return out, true, nil
	default:
		return nil, false, errors.New("scram: Step called on finished exchange")
	}
}

// handleClientFirst parses the client-first-message and produces the
// server-first-message.
//
// client-first-message: gs2-cbind-flag "," [authzid] "," username-attr "," nonce-attr
// e.g. "n,,n=alice,r=clientNonce"
//
// server-first-message: r=clientNonce+serverNonce,s=base64(salt),i=iterations
func (s *SCRAMServer) handleClientFirst(input []byte) ([]byte, error) {
	// gs2-cbind-flag is one of "n", "y", or "p=...". Everything from
	// the leading channel-binding flag through the second comma is the
	// gs2 header; the rest is the client-first-message-bare.
	parts := strings.SplitN(string(input), ",", 3)
	if len(parts) < 3 {
		return nil, fmt.Errorf("scram: malformed client-first-message")
	}
	cbindFlag := parts[0]
	switch cbindFlag {
	case "n", "y":
		// Acceptable: "n" = client doesn't support binding;
		// "y" = client supports binding but server didn't advertise PLUS.
	default:
		// "p=..." (channel binding requested) is not supported until
		// TLS lands; reject explicitly.
		if strings.HasPrefix(cbindFlag, "p=") {
			return nil, fmt.Errorf("scram: channel binding requested but not yet supported by goopg")
		}
		return nil, fmt.Errorf("scram: bad gs2-cbind-flag %q", cbindFlag)
	}
	// authzid (parts[1]) is allowed to be empty; we ignore non-empty
	// values matching upstream which uses the StartupMessage user.
	bare := parts[2]
	clientFirstBare := bare

	// Parse n=... ,r=...
	attrs, err := parseSCRAMAttrs(bare)
	if err != nil {
		return nil, err
	}
	// Some clients send "m=..." mandatory extensions which we reject.
	if _, hasM := attrs["m"]; hasM {
		return nil, errors.New("scram: mandatory extensions not supported")
	}
	clientNonce, ok := attrs["r"]
	if !ok || clientNonce == "" {
		return nil, errors.New("scram: missing client nonce")
	}
	s.clientNonce = clientNonce

	combined := s.clientNonce + s.serverNonce
	serverFirst := fmt.Sprintf("r=%s,s=%s,i=%d",
		combined, s.secret.EncodedSalt(), s.secret.Iterations)
	s.authMessage = clientFirstBare + "," + serverFirst
	return []byte(serverFirst), nil
}

// handleClientFinal parses client-final-message and verifies the proof.
//
// client-final-message: c=base64(gs2-header),r=combinedNonce,p=base64(ClientProof)
// server-final-message: v=base64(ServerSignature)
func (s *SCRAMServer) handleClientFinal(input []byte) ([]byte, error) {
	body := string(input)
	// Split off the proof attribute first: it is always last.
	withoutProof, proofPart, ok := cutLastAttr(body, "p=")
	if !ok {
		return nil, errors.New("scram: client-final-message missing proof")
	}
	attrs, err := parseSCRAMAttrs(withoutProof)
	if err != nil {
		return nil, err
	}
	channel, ok := attrs["c"]
	if !ok {
		return nil, errors.New("scram: missing c= channel attribute")
	}
	if !validNoBindingChannelAttr(channel) {
		return nil, errors.New("scram: unsupported channel-binding response")
	}
	r, ok := attrs["r"]
	if !ok || r != s.clientNonce+s.serverNonce {
		return nil, errors.New("scram: nonce mismatch")
	}
	clientProofB64 := strings.TrimPrefix(proofPart, "p=")
	clientProof, err := base64.StdEncoding.DecodeString(clientProofB64)
	if err != nil || len(clientProof) != scramKeyLen {
		return nil, errors.New("scram: bad ClientProof base64")
	}

	s.authMessage += "," + withoutProof
	clientSignature := hmacSHA256(s.secret.StoredKey, []byte(s.authMessage))
	clientKey := xor(clientProof, clientSignature)
	storedKeyCandidate := sha256Sum(clientKey)
	if subtle.ConstantTimeCompare(storedKeyCandidate, s.secret.StoredKey) != 1 || s.doomed {
		return nil, ErrInvalidPassword{User: s.username}
	}

	serverSignature := hmacSHA256(s.secret.ServerKey, []byte(s.authMessage))
	out := "v=" + base64.StdEncoding.EncodeToString(serverSignature)
	return []byte(out), nil
}

// parseSCRAMAttrs splits a SCRAM attribute string ("k=v,k=v,...") into
// a map. Order is irrelevant; duplicate keys are an error.
func parseSCRAMAttrs(s string) (map[string]string, error) {
	out := map[string]string{}
	for _, attr := range strings.Split(s, ",") {
		if attr == "" {
			continue
		}
		k, v, ok := strings.Cut(attr, "=")
		if !ok || len(k) != 1 {
			return nil, fmt.Errorf("scram: bad attribute %q", attr)
		}
		if _, dup := out[k]; dup {
			return nil, fmt.Errorf("scram: duplicate attribute %q", k)
		}
		out[k] = v
	}
	return out, nil
}

// cutLastAttr splits s around the *last* occurrence of "<prefix>"
// followed by an attribute body, returning everything before, the
// matched attribute, and ok. Used to peel the trailing "p=..." off the
// client-final-message.
func cutLastAttr(s, prefix string) (before, attr string, ok bool) {
	// Find ",p=" (or "p=" at the start).
	needle := "," + prefix
	idx := strings.LastIndex(s, needle)
	if idx == -1 {
		if strings.HasPrefix(s, prefix) {
			return "", s, true
		}
		return "", "", false
	}
	return s[:idx], s[idx+1:], true
}

// validNoBindingChannelAttr accepts the c= values RFC 5802 permits when
// no channel binding was negotiated:
//   - base64("n,,") for client gs2-cbind-flag "n"
//   - base64("y,,") for client gs2-cbind-flag "y"
//
// Anything else (a "p=..." gs2 header, an unexpected authzid) is
// rejected.
func validNoBindingChannelAttr(c string) bool {
	bytes, err := base64.StdEncoding.DecodeString(c)
	if err != nil {
		return false
	}
	switch string(bytes) {
	case "n,,", "y,,":
		return true
	}
	return false
}

// PBKDF2-HMAC-SHA-256, the simplest possible implementation. The Go
// standard library does not export PBKDF2 as of Go 1.22, so we
// hand-roll it; the implementation is small and the test against a
// known answer pins it.
func pbkdf2HMACSHA256(password, salt []byte, iter, keyLen int) []byte {
	hashLen := sha256.Size
	numBlocks := (keyLen + hashLen - 1) / hashLen
	out := make([]byte, 0, numBlocks*hashLen)
	for i := 1; i <= numBlocks; i++ {
		// U_1 = HMAC(password, salt || INT(i))
		u := hmacSHA256Init(password)
		u.Write(salt)
		var ibuf [4]byte
		ibuf[0] = byte(i >> 24)
		ibuf[1] = byte(i >> 16)
		ibuf[2] = byte(i >> 8)
		ibuf[3] = byte(i)
		u.Write(ibuf[:])
		ui := u.Sum(nil)
		block := append([]byte(nil), ui...)
		// U_j = HMAC(password, U_{j-1}); T_i = U_1 XOR U_2 XOR ...
		for j := 2; j <= iter; j++ {
			h := hmacSHA256Init(password)
			h.Write(ui)
			ui = h.Sum(nil)
			for k := range block {
				block[k] ^= ui[k]
			}
		}
		out = append(out, block...)
	}
	return out[:keyLen]
}

// hmacSHA256Init returns a fresh HMAC-SHA-256 hash.Hash for `key`.
func hmacSHA256Init(key []byte) interface {
	Write(p []byte) (n int, err error)
	Sum(b []byte) []byte
	Reset()
	Size() int
} {
	return hmac.New(sha256.New, key)
}

func hmacSHA256(key, msg []byte) []byte {
	h := hmac.New(sha256.New, key)
	h.Write(msg)
	return h.Sum(nil)
}

func sha256Sum(msg []byte) []byte {
	sum := sha256.Sum256(msg)
	return sum[:]
}

func xor(a, b []byte) []byte {
	out := make([]byte, len(a))
	for i := range a {
		out[i] = a[i] ^ b[i]
	}
	return out
}
