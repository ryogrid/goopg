package initdb

import (
	"fmt"
	"os"
	"slices"
	"strings"

	"github.com/goopg/goopg/internal/libpq/auth"
)

// validAuthMethodsHost is the set of authentication methods accepted for
// "host" connections, mirroring upstream initdb's auth_methods_host[]
// (initdb.c:96). The build-gated methods (gss, sspi, pam, bsd, ldap, cert)
// are accepted as valid names so an operator can request any method a real
// pg_hba.conf may carry; goopg's connection-time auth (internal/auth) only
// enforces trust/reject in v0, exactly as it already does for hand-written
// pg_hba.conf files. initdb's job here is solely to write the file.
var validAuthMethodsHost = []string{
	"trust", "reject", "scram-sha-256", "md5", "password", "ident", "radius",
	"gss", "sspi", "pam", "bsd", "ldap", "cert",
}

// validAuthMethodsLocal mirrors auth_methods_local[] (initdb.c:118): same as
// host but with "peer" instead of "ident", and without "cert".
var validAuthMethodsLocal = []string{
	"trust", "reject", "scram-sha-256", "md5", "password", "peer", "radius",
	"gss", "sspi", "pam", "bsd", "ldap",
}

// isPasswordAuthMethod reports whether a method requires the superuser to
// have a password set (initdb.c check_need_password, 2596).
func isPasswordAuthMethod(m string) bool {
	return m == "md5" || m == "password" || m == "scram-sha-256"
}

func authMethodValid(m string, valid []string) bool {
	return slices.Contains(valid, m)
}

// resolveAuthMethods ports the auth-method portion of initdb's option
// handling: default-to-trust (with a warning), the ident↔peer cross-map,
// per-conntype validation, and the must-have-password check.
//
// host and local are the raw Options values (empty when unspecified). The
// ident↔peer cross-map (initdb.c:3255-3258) is a parse-time effect of a
// single -A/--auth setting *both* sides to the same value, so it only fires
// when host == local (which includes the both-empty default case before
// defaulting). hasPassword reports whether --pwfile (or an interactive
// prompt, which goopg does not support) supplied a superuser password.
//
// It returns the resolved host/local methods, a warn flag set when no method
// was specified (so the caller can emit the trust-default warning), and an
// error for an invalid method or a password method without a password.
func resolveAuthMethods(host, local string, hasPassword bool) (rHost, rLocal string, warn bool, err error) {
	rHost, rLocal = host, local

	// ident↔peer cross-map: only when a single --auth set both sides equal.
	// (initdb.c maps at parse time before either could be overridden by the
	// per-side options.)
	if rHost == rLocal {
		switch rHost {
		case "ident":
			rLocal = "peer"
		case "peer":
			rHost = "ident"
		}
	}

	// check_authmethod_unspecified (initdb.c:2571): default trust + warn.
	if rHost == "" {
		warn = true
		rHost = "trust"
	}
	if rLocal == "" {
		warn = true
		rLocal = "trust"
	}

	// check_authmethod_valid (initdb.c:2581).
	if !authMethodValid(rHost, validAuthMethodsHost) {
		return "", "", false, fmt.Errorf("goopg init: invalid authentication method %q for \"host\" connections", rHost)
	}
	if !authMethodValid(rLocal, validAuthMethodsLocal) {
		return "", "", false, fmt.Errorf("goopg init: invalid authentication method %q for \"local\" connections", rLocal)
	}

	// check_need_password (initdb.c:2596): both sides password-based and no
	// password supplied.
	if isPasswordAuthMethod(rHost) && isPasswordAuthMethod(rLocal) && !hasPassword {
		return "", "", false, fmt.Errorf("goopg init: must specify a password for the superuser to enable password authentication")
	}

	return rHost, rLocal, warn, nil
}

// buildPgHBAConf renders pg_hba.conf with the chosen host/local methods
// substituted into the local and loopback-host rules. The external
// catch-all rules stay reject (goopg's default-deny policy for non-loopback
// connections, unchanged from the original fixed template).
//
// The three `replication` rules mirror upstream's pg_hba.conf.sample
// (postgres/src/backend/libpq/pg_hba.conf.sample, emitted verbatim by
// initdb's setup_config) — `local replication all <local>`, plus the
// 127.0.0.1/32 and ::1/128 host twins. In upstream a DATABASE field of
// `all` does NOT match a physical replication connection (hba.c:
// check_hba → check_db), so without these rules a replication walreceiver
// gets "no pg_hba.conf entry for replication connection". goopg never
// noticed because its own matcher treats `all` as matching everything
// (internal/auth.nameListMatches); it only became visible once a real
// PG 18.3 was started on a goopg-initdb'd data directory and read this
// file (M0130-S10 Phase D pg_basebackup).
func buildPgHBAConf(hostMethod, localMethod string) []byte {
	return fmt.Appendf(nil, `# goopg pg_hba.conf — host-based authentication rules.
#
# Same format as upstream PostgreSQL: TYPE  DATABASE  USER  ADDRESS  METHOD
# The first matching rule wins. Default policy: trust loopback,
# reject everything else.

# TYPE   DATABASE  USER   ADDRESS         METHOD
local    all       all                    %[2]s
host     all       all    127.0.0.1/32    %[1]s
host     all       all    ::1/128         %[1]s

# Allow replication connections from localhost, by a user with the
# replication privilege. The "all" DATABASE keyword does not cover
# replication connections, so these rules are required for a walreceiver
# or pg_basebackup to connect.
local    replication  all                 %[2]s
host     replication  all  127.0.0.1/32   %[1]s
host     replication  all  ::1/128        %[1]s

host     all       all    0.0.0.0/0       reject
host     all       all    ::/0            reject
`, hostMethod, localMethod)
}

// readSuperuserPasswordFile reads the superuser password from the first line
// of path, stripping a trailing CR/LF, mirroring initdb's get_su_pwd file
// branch (initdb.c:1689-1707): an unreadable file or an empty file is an
// error.
func readSuperuserPasswordFile(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("goopg init: could not open file %q for reading: %w", path, err)
	}
	text := string(data)
	// First line only (pg_get_line reads up to the first newline).
	if i := strings.IndexByte(text, '\n'); i >= 0 {
		text = text[:i]
	}
	// pg_strip_crlf: trim a trailing \r left by a CRLF file.
	text = strings.TrimRight(text, "\r")
	if text == "" {
		return "", fmt.Errorf("goopg init: password file %q is empty", path)
	}
	return text, nil
}

// encodeSuperuserPassword encodes a cleartext superuser password into the
// pg_authid.rolpassword verifier form, choosing md5 vs scram-sha-256 the same
// way initdb's setup_config does (initdb.c:1402-1413): md5 is used only when
// an md5 auth method was chosen and the other side is not scram-sha-256;
// otherwise the cluster default (scram-sha-256) applies.
//
// It returns the verifier string to store in rolpassword and the
// password_encryption GUC value to seed into postgresql.conf — non-empty
// ("md5") only when md5 encryption is selected, since scram-sha-256 is the
// template default and needs no override.
func encodeSuperuserPassword(password, hostMethod, localMethod, username string) (verifier, passwordEncryption string, err error) {
	useMD5 := (localMethod == "md5" && hostMethod != "scram-sha-256") ||
		(hostMethod == "md5" && localMethod != "scram-sha-256")
	if useMD5 {
		return auth.MD5Shadow(password, username), "md5", nil
	}
	secret, err := auth.NewSCRAMSecret(password)
	if err != nil {
		return "", "", fmt.Errorf("goopg init: encode superuser password: %w", err)
	}
	return secret.String(), "", nil
}
