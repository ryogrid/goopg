package auth

import (
	"bufio"
	"crypto/md5"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
)

// UserStore answers "does this user exist, and what is its rolpassword?".
//
// The interface is the only thing v0 needs — a real catalog-backed store
// drops in once the catalog exists. The verb names mirror upstream's
// rolpassword semantics so callers can stay close to PostgreSQL when
// reasoning about behaviour.
type UserStore interface {
	// Lookup returns the credential for user, and (Credential{}, false) if
	// no such user exists. Implementations must not return an error for
	// "user does not exist" — return ok=false instead.
	Lookup(user string) (Credential, bool)
}

// PasswordType is the format of the secret stored in a Credential.
//
// Mirrors upstream's PasswordType enum
// (postgres/src/backend/libpq/crypt.c:get_password_type), restricted to
// the formats goopg understands.
type PasswordType int

const (
	// PasswordPlaintext is "alice secretpw" — the secret is the literal
	// password. Not appropriate for production but unavoidable for
	// development and for testing the cleartext "password" auth method.
	PasswordPlaintext PasswordType = iota

	// PasswordMD5 is "alice md5<32 hex chars>" — the secret is
	// `"md5" + md5_hex(password + username)`. This is the legacy
	// pg_authid.rolpassword shape that the `md5` auth method consumes
	// directly without ever seeing the original password.
	PasswordMD5

	// PasswordSCRAMSHA256 stores the upstream
	//   SCRAM-SHA-256$<iter>:<salt-b64>$<StoredKey-b64>:<ServerKey-b64>
	// rolpassword form. The cleartext password is unrecoverable from
	// this format; it satisfies the `scram-sha-256` auth method
	// directly, and the `password` (cleartext) method by recomputing
	// the StoredKey and comparing.
	PasswordSCRAMSHA256
)

// Credential is one entry in the user store. Use NewPlaintextCredential or
// NewMD5Credential to construct one rather than building it by hand —
// the constructors validate the secret format.
type Credential struct {
	Type   PasswordType
	Secret string // plaintext password OR "md5"+32-hex-MD5 string
}

// NewPlaintextCredential is the obvious constructor.
func NewPlaintextCredential(password string) Credential {
	return Credential{Type: PasswordPlaintext, Secret: password}
}

// NewMD5Credential takes the *cleartext* password and computes the
// "md5"+hex(md5(password+username)) shadow form. Pass an already-shadowed
// secret through ParseMD5Stored if you have one verbatim.
func NewMD5Credential(user, password string) Credential {
	return Credential{
		Type:   PasswordMD5,
		Secret: md5Shadow(password, user),
	}
}

// ParseMD5Stored takes a secret that already looks like
// "md5<32-hex-chars>" and validates it. Used to build a UserStore from a
// pre-shadowed credential file.
func ParseMD5Stored(secret string) (Credential, error) {
	if !strings.HasPrefix(secret, "md5") || len(secret) != 35 {
		return Credential{}, fmt.Errorf("md5 stored secret must be 'md5'+32 hex chars, got %d chars", len(secret))
	}
	if _, err := hex.DecodeString(secret[3:]); err != nil {
		return Credential{}, fmt.Errorf("md5 stored secret has non-hex tail: %w", err)
	}
	return Credential{Type: PasswordMD5, Secret: secret}, nil
}

// VerifyCleartext checks an incoming cleartext password against the stored
// credential. Plaintext credentials are compared byte-for-byte
// (constant-time); md5 credentials are reproduced by re-shadowing the
// supplied password+username; SCRAM credentials are validated by re-running
// the SCRAM key derivation and comparing StoredKey, matching upstream's
// scram_verify_plain_password.
func (c Credential) VerifyCleartext(user, given string) bool {
	switch c.Type {
	case PasswordPlaintext:
		return subtle.ConstantTimeCompare([]byte(c.Secret), []byte(given)) == 1
	case PasswordMD5:
		return subtle.ConstantTimeCompare([]byte(c.Secret), []byte(md5Shadow(given, user))) == 1
	case PasswordSCRAMSHA256:
		secret, err := ParseSCRAMSecret(c.Secret)
		if err != nil {
			return false
		}
		return secret.VerifySCRAMSecretFromPassword(given)
	default:
		return false
	}
}

// VerifyMD5Challenge checks the response a client sent to an
// AuthenticationMD5Password challenge. The wire form is
// "md5" + md5_hex(rolpassword_md5_tail + salt), where rolpassword_md5_tail
// is the 32 hex chars after the "md5" prefix in the stored secret. See
// postgres/src/backend/libpq/crypt.c:md5_crypt_verify and
// postgres/src/common/md5_common.c:pg_md5_encrypt for the upstream
// reference.
//
// A plaintext credential works too: we shadow the stored password to
// derive the md5 form on the fly. A real PostgreSQL deployment will
// usually have md5-shadowed secrets, but the convenience matters for
// tests and for operators dropping a quick `goopg-users.txt` next to
// the binary.
func (c Credential) VerifyMD5Challenge(user string, salt [4]byte, response string) bool {
	var stored string
	switch c.Type {
	case PasswordMD5:
		stored = c.Secret
	case PasswordPlaintext:
		stored = md5Shadow(c.Secret, user)
	default:
		return false
	}
	expected := "md5" + md5Hex(stored[3:]+string(salt[:]))
	return subtle.ConstantTimeCompare([]byte(expected), []byte(response)) == 1
}

// md5Shadow computes the upstream rolpassword-style "md5HEX" form for
// (password, username). Equivalent to pg_md5_encrypt(password+username).
func md5Shadow(password, user string) string {
	return "md5" + md5Hex(password+user)
}

func md5Hex(s string) string {
	sum := md5.Sum([]byte(s))
	return hex.EncodeToString(sum[:])
}

// MapUserStore is the obvious in-memory implementation of UserStore. It
// is concurrency-safe; concurrent reads are common after the policy
// settles, and writes are rare (catalog reload). A real catalog-backed
// store will replace this once the catalog exists.
type MapUserStore struct {
	mu    sync.RWMutex
	users map[string]Credential
}

// NewMapUserStore constructs an empty store.
func NewMapUserStore() *MapUserStore {
	return &MapUserStore{users: map[string]Credential{}}
}

// Set replaces (or inserts) the credential for a user.
func (m *MapUserStore) Set(user string, c Credential) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.users[user] = c
}

// Lookup implements UserStore.
func (m *MapUserStore) Lookup(user string) (Credential, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	c, ok := m.users[user]
	return c, ok
}

// LoadUsersFile reads a pg_auth-style credential file and returns a
// populated MapUserStore. Each non-blank, non-comment line has the form:
//
//	username:secret
//
// where secret is one of:
//   - a plaintext password (no prefix)
//   - "md5<32 hex chars>" (pre-shadowed MD5 verifier)
//   - "SCRAM-SHA-256$…" (upstream SCRAM verifier string)
//
// Lines beginning with '#' are comments and are silently skipped.
// Returns an error if the file cannot be read or a line is malformed.
func LoadUsersFile(path string) (*MapUserStore, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	store := NewMapUserStore()
	scanner := bufio.NewScanner(f)
	lineNo := 0
	for scanner.Scan() {
		lineNo++
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		idx := strings.IndexByte(line, ':')
		if idx < 1 {
			return nil, fmt.Errorf("pg_auth line %d: expected 'username:secret' format", lineNo)
		}
		username := line[:idx]
		secret := line[idx+1:]
		var cred Credential
		switch {
		case strings.HasPrefix(secret, "SCRAM-SHA-256$"):
			if _, err := ParseSCRAMSecret(secret); err != nil {
				return nil, fmt.Errorf("pg_auth line %d: invalid SCRAM secret: %w", lineNo, err)
			}
			cred = Credential{Type: PasswordSCRAMSHA256, Secret: secret}
		case strings.HasPrefix(secret, "md5") && len(secret) == 35:
			if c, err := ParseMD5Stored(secret); err != nil {
				return nil, fmt.Errorf("pg_auth line %d: invalid MD5 secret: %w", lineNo, err)
			} else {
				cred = c
			}
		default:
			cred = NewPlaintextCredential(secret)
		}
		store.Set(username, cred)
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("pg_auth: %w", err)
	}
	return store, nil
}

// ErrInvalidPassword is returned when password / md5 verification fails.
// It carries the user name so callers can log a useful diagnostic
// without leaking the supplied password into the log.
type ErrInvalidPassword struct {
	User string
}

func (e ErrInvalidPassword) Error() string {
	return fmt.Sprintf("password authentication failed for user %q", e.User)
}

// ErrUserUnknown is returned when the user store has no entry. The
// returned message intentionally matches ErrInvalidPassword's so that
// network observers can't distinguish "no such user" from "wrong
// password" by error text alone (mirroring upstream's posture in
// postgres/src/backend/libpq/auth.c:auth_failed).
type ErrUserUnknown struct {
	User string
}

func (e ErrUserUnknown) Error() string {
	return fmt.Sprintf("password authentication failed for user %q", e.User)
}

// ErrAuthExchange wraps an I/O error that prevented us from completing
// the password exchange (e.g. the client disconnected after we sent the
// AuthenticationRequest).
type ErrAuthExchange struct{ Err error }

func (e ErrAuthExchange) Error() string { return "auth exchange: " + e.Err.Error() }
func (e ErrAuthExchange) Unwrap() error { return e.Err }

// Sentinel for the rare case where the client sends something other
// than a PasswordMessage in response to an auth challenge.
var ErrUnexpectedFrame = errors.New("expected PasswordMessage frame")
