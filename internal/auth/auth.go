// Package auth implements goopg's pg_hba.conf-based authentication policy.
//
// Scope and growth path are documented in docs/design/0003-authentication.md.
// v0 implements the parser + matcher + the `trust` and `reject` outcomes;
// password / md5 / scram-sha-256 land in subsequent loops as additional
// case arms in Authenticate.
package auth

import (
	"errors"
	"fmt"
	"net"
)

// Method is the authentication method named in a pg_hba.conf entry.
//
// The full set mirrors postgres/src/include/libpq/hba.h:UserAuth so the
// parser can read any real pg_hba.conf without complaint. Only Trust and
// Reject (and the implicit reject) are actually applied in v0; everything
// else is acknowledged with a "feature not supported" error at
// authentication time.
type Method int

const (
	MethodImplicitReject Method = iota // server-emitted when no rule matches
	MethodReject
	MethodTrust
	MethodPassword
	MethodMD5
	MethodSCRAMSHA256
	MethodGSS
	MethodSSPI
	MethodIdent
	MethodPeer
	MethodCert
	MethodPAM
	MethodBSD
	MethodLDAP
	MethodRADIUS
	MethodOAuth
)

// methodNames maps the keyword in pg_hba.conf to the Method enum. The
// "scram-sha-256" form is the canonical one in upstream; we keep it
// lowercase exactly so error messages echo what the operator wrote.
var methodNames = map[string]Method{
	"reject":        MethodReject,
	"trust":         MethodTrust,
	"password":      MethodPassword,
	"md5":           MethodMD5,
	"scram-sha-256": MethodSCRAMSHA256,
	"gss":           MethodGSS,
	"sspi":          MethodSSPI,
	"ident":         MethodIdent,
	"peer":          MethodPeer,
	"cert":          MethodCert,
	"pam":           MethodPAM,
	"bsd":           MethodBSD,
	"ldap":          MethodLDAP,
	"radius":        MethodRADIUS,
	"oauth":         MethodOAuth,
}

// String returns the canonical pg_hba.conf keyword for the method, or
// "implicit-reject" for MethodImplicitReject.
func (m Method) String() string {
	for k, v := range methodNames {
		if v == m {
			return k
		}
	}
	if m == MethodImplicitReject {
		return "implicit-reject"
	}
	return fmt.Sprintf("Method(%d)", int(m))
}

// ConnType is the leading keyword on a pg_hba.conf line.
type ConnType int

const (
	ConnLocal        ConnType = iota // Unix domain socket
	ConnHost                         // TCP, encryption-agnostic
	ConnHostSSL                      // TCP, must be TLS
	ConnHostNoSSL                    // TCP, must be plaintext
	ConnHostGSSEnc                   // recognised but not applied in v0
	ConnHostNoGSSEnc                 // recognised but not applied in v0
)

var connTypeNames = map[string]ConnType{
	"local":        ConnLocal,
	"host":         ConnHost,
	"hostssl":      ConnHostSSL,
	"hostnossl":    ConnHostNoSSL,
	"hostgssenc":   ConnHostGSSEnc,
	"hostnogssenc": ConnHostNoGSSEnc,
}

// String returns the canonical keyword.
func (c ConnType) String() string {
	for k, v := range connTypeNames {
		if v == c {
			return k
		}
	}
	return fmt.Sprintf("ConnType(%d)", int(c))
}

// Rule is a single parsed pg_hba.conf line.
//
// Database and User are pre-split lists; the special keywords ("all",
// "sameuser", "samerole", "replication", "+groupname", "/regex") survive as
// literal entries in the slice and are interpreted by the matcher.
type Rule struct {
	ConnType ConnType
	Database []string
	User     []string

	// Address constraints. Network is non-nil for TCP rules; AllAddresses
	// is true when the rule used the "all" keyword (which matches any
	// remote address). SameHost / SameNet are recognised but not yet
	// implemented; rules using them never match in v0.
	Network      *net.IPNet
	AllAddresses bool
	SameHost     bool
	SameNet      bool

	Method  Method
	Options map[string]string

	// SourceFile / SourceLine are diagnostics for the matcher's error
	// messages; they help operators trace which rule decided a match.
	SourceFile string
	SourceLine int
}

// Request is what we feed the matcher: everything it needs to make a
// decision, sourced from the connection metadata and the StartupMessage.
type Request struct {
	ConnType ConnType
	Remote   net.IP // nil for ConnLocal
	Database string
	User     string
}

// Decision is what the matcher returns. The pointer to the matched Rule
// (or nil for an implicit reject) is exposed so callers can produce
// diagnostics like "rejected by line 7 of pg_hba.conf".
type Decision struct {
	Method  Method
	Options map[string]string
	Rule    *Rule
}

// Policy is the matcher interface. It is satisfied by RuleSet (the parser
// output) and by anything else tests want to inject.
type Policy interface {
	Match(req Request) Decision
}

// RuleSet is the parsed pg_hba.conf, kept in source order. It satisfies
// Policy and is what file-driven configurations produce.
type RuleSet struct {
	Rules []Rule
}

// Match walks rules in order and returns the first match. If nothing
// matches, the returned Decision has Method == MethodImplicitReject and
// Rule == nil — mirroring upstream check_hba's behavior.
func (rs *RuleSet) Match(req Request) Decision {
	for i := range rs.Rules {
		r := &rs.Rules[i]
		if matches(r, req) {
			return Decision{
				Method:  r.Method,
				Options: r.Options,
				Rule:    r,
			}
		}
	}
	return Decision{Method: MethodImplicitReject}
}

// DefaultPolicy returns the v0 default policy: trust loopback IPv4 and
// IPv6, implicit-reject everything else. Documented in
// docs/design/0003-authentication.md.
func DefaultPolicy() *RuleSet {
	_, ipv4loop, _ := net.ParseCIDR("127.0.0.1/32")
	_, ipv6loop, _ := net.ParseCIDR("::1/128")
	return &RuleSet{Rules: []Rule{
		{
			ConnType:   ConnHost,
			Database:   []string{"all"},
			User:       []string{"all"},
			Network:    ipv4loop,
			Method:     MethodTrust,
			SourceFile: "<built-in default policy>",
			SourceLine: 1,
		},
		{
			ConnType:   ConnHost,
			Database:   []string{"all"},
			User:       []string{"all"},
			Network:    ipv6loop,
			Method:     MethodTrust,
			SourceFile: "<built-in default policy>",
			SourceLine: 2,
		},
	}}
}

func matches(r *Rule, req Request) bool {
	if !connTypeMatches(r.ConnType, req.ConnType) {
		return false
	}
	if !nameListMatches(r.Database, req.Database, true /*allow special keywords*/) {
		return false
	}
	if !nameListMatches(r.User, req.User, false) {
		return false
	}
	if req.ConnType == ConnLocal {
		// `local` rules don't match TCP traffic and vice versa; address
		// constraints are nonsensical for ConnLocal so we always pass.
		return r.ConnType == ConnLocal
	}
	return addressMatches(r, req.Remote)
}

func connTypeMatches(rule, req ConnType) bool {
	switch rule {
	case ConnLocal:
		return req == ConnLocal
	case ConnHost:
		return req == ConnHost || req == ConnHostSSL || req == ConnHostNoSSL
	case ConnHostSSL:
		return req == ConnHostSSL
	case ConnHostNoSSL:
		return req == ConnHost || req == ConnHostNoSSL
	case ConnHostGSSEnc, ConnHostNoGSSEnc:
		// v0 has no GSS-encrypted connections, so these never apply.
		return false
	}
	return false
}

func nameListMatches(rule []string, requested string, allowDBSpecials bool) bool {
	for _, n := range rule {
		switch {
		case n == "all":
			return true
		case allowDBSpecials && (n == "sameuser" || n == "samerole" || n == "replication"):
			// Specials that need session/role data — punt for now.
			// `replication` only matches if the requested DB literally equals
			// "replication"; treat it conservatively.
			if n == "replication" && requested == "replication" {
				return true
			}
			continue
		case n == requested:
			return true
		}
	}
	return false
}

func addressMatches(r *Rule, ip net.IP) bool {
	if r.AllAddresses {
		return true
	}
	if r.SameHost || r.SameNet {
		// Not implemented in v0 — fail closed rather than match by
		// accident. Operators using these keywords get an implicit reject
		// and can pick a CIDR if they need this connection through.
		return false
	}
	if r.Network == nil {
		return false
	}
	if ip == nil {
		return false
	}
	return r.Network.Contains(ip)
}

// Authenticate runs the authentication exchange for a Decision. v0 only
// implements MethodTrust and the two reject methods; other methods cause
// it to return an error so handleStartup can emit a FATAL ErrorResponse.
//
// The caller is responsible for actually writing AuthenticationOk / the
// FATAL ErrorResponse on the wire — Authenticate just decides whether
// the handshake should proceed and returns a typed reason if not.
func Authenticate(d Decision) error {
	switch d.Method {
	case MethodTrust:
		return nil
	case MethodReject, MethodImplicitReject:
		return ErrRejected{Decision: d}
	default:
		return ErrMethodUnsupported{Method: d.Method}
	}
}

// ErrRejected is returned when the matched rule is `reject` or no rule
// matched (implicit reject).
type ErrRejected struct {
	Decision Decision
}

func (e ErrRejected) Error() string {
	if e.Decision.Rule == nil {
		return "no pg_hba.conf entry matched the connection"
	}
	return fmt.Sprintf("connection rejected by pg_hba rule at %s:%d",
		e.Decision.Rule.SourceFile, e.Decision.Rule.SourceLine)
}

// ErrMethodUnsupported is returned for methods the parser recognised but
// goopg has not yet implemented.
type ErrMethodUnsupported struct {
	Method Method
}

func (e ErrMethodUnsupported) Error() string {
	return fmt.Sprintf("authentication method %q is not yet implemented in goopg", e.Method.String())
}

// ParseError annotates failures from the file parser with file/line context.
type ParseError struct {
	File string
	Line int
	Err  error
}

func (e ParseError) Error() string {
	if e.File == "" {
		return fmt.Sprintf("line %d: %v", e.Line, e.Err)
	}
	return fmt.Sprintf("%s:%d: %v", e.File, e.Line, e.Err)
}

func (e ParseError) Unwrap() error { return e.Err }

// ErrUnsupportedSyntax is returned for syntax the parser recognises but
// does not yet evaluate (regex names, @file includes in DB/user lists,
// hostnames in the address field). The matcher fails closed for any rule
// constructed from such syntax — see the doc for the policy.
var ErrUnsupportedSyntax = errors.New("syntax not yet supported in goopg")
