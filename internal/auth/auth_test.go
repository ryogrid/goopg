package auth

import (
	"errors"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestParseMinimalHBA covers the v0 file-format expectations from
// docs/design/0003-authentication.md: leading host rule with a CIDR address,
// trust method, options ignored gracefully.
func TestParseMinimalHBA(t *testing.T) {
	src := `
# A pg_hba.conf for tests.
host all all 127.0.0.1/32 trust
host all alice 192.168.1.0/24 reject
local all all trust
`
	rs, err := ParseHBAReader(strings.NewReader(src), "test.conf")
	if err != nil {
		t.Fatal(err)
	}
	if len(rs.Rules) != 3 {
		t.Fatalf("got %d rules, want 3", len(rs.Rules))
	}
	if rs.Rules[0].ConnType != ConnHost || rs.Rules[0].Method != MethodTrust {
		t.Errorf("rule 0: %+v", rs.Rules[0])
	}
	if rs.Rules[0].Network == nil || rs.Rules[0].Network.String() != "127.0.0.1/32" {
		t.Errorf("rule 0 network = %v, want 127.0.0.1/32", rs.Rules[0].Network)
	}
	if rs.Rules[1].User[0] != "alice" || rs.Rules[1].Method != MethodReject {
		t.Errorf("rule 1: %+v", rs.Rules[1])
	}
	if rs.Rules[2].ConnType != ConnLocal || rs.Rules[2].Method != MethodTrust {
		t.Errorf("rule 2: %+v", rs.Rules[2])
	}
}

func TestParseRejectsBadMethod(t *testing.T) {
	_, err := ParseHBAReader(strings.NewReader("host all all 0.0.0.0/0 frobozz"), "")
	if err == nil {
		t.Fatal("expected parse error for unknown method")
	}
	var pe ParseError
	if !errors.As(err, &pe) {
		t.Fatalf("expected ParseError, got %T: %v", err, err)
	}
	if pe.Line != 1 {
		t.Errorf("line = %d, want 1", pe.Line)
	}
}

func TestParseAddressFormats(t *testing.T) {
	cases := []struct {
		line   string
		want   string // empty means we don't assert
		errMsg string
	}{
		{"host all all 0.0.0.0/0 trust", "0.0.0.0/0", ""},
		{"host all all ::/0 trust", "::/0", ""},
		{"host all all 10.0.0.0 255.255.255.0 trust", "10.0.0.0/24", ""},
		{"host all all all trust", "", ""}, // AllAddresses keyword
	}
	for _, tc := range cases {
		t.Run(tc.line, func(t *testing.T) {
			rs, err := ParseHBAReader(strings.NewReader(tc.line), "")
			if err != nil {
				t.Fatal(err)
			}
			if len(rs.Rules) != 1 {
				t.Fatalf("got %d rules", len(rs.Rules))
			}
			r := rs.Rules[0]
			if tc.want == "" {
				if !r.AllAddresses && r.Network == nil {
					return
				}
				if r.AllAddresses {
					return
				}
				t.Fatalf("expected AllAddresses, got network=%v", r.Network)
			}
			if r.Network == nil {
				t.Fatalf("expected network %s, got nil", tc.want)
			}
			if got := r.Network.String(); got != tc.want {
				t.Errorf("network = %s, want %s", got, tc.want)
			}
		})
	}
}

// TestMatchOrder ensures first-match semantics: a reject before a trust
// for the same tuple wins.
func TestMatchOrder(t *testing.T) {
	src := `
host all alice 127.0.0.1/32 reject
host all all   127.0.0.1/32 trust
`
	rs, err := ParseHBAReader(strings.NewReader(src), "")
	if err != nil {
		t.Fatal(err)
	}
	dec := rs.Match(Request{
		ConnType: ConnHost,
		Remote:   net.ParseIP("127.0.0.1"),
		Database: "postgres",
		User:     "alice",
	})
	if dec.Method != MethodReject {
		t.Fatalf("alice -> %s, want reject", dec.Method)
	}

	dec = rs.Match(Request{
		ConnType: ConnHost,
		Remote:   net.ParseIP("127.0.0.1"),
		Database: "postgres",
		User:     "bob",
	})
	if dec.Method != MethodTrust {
		t.Fatalf("bob -> %s, want trust", dec.Method)
	}
}

// TestImplicitReject covers the contract that no-match is a reject.
func TestImplicitReject(t *testing.T) {
	rs, err := ParseHBAReader(strings.NewReader("host all all 127.0.0.1/32 trust"), "")
	if err != nil {
		t.Fatal(err)
	}
	dec := rs.Match(Request{
		ConnType: ConnHost,
		Remote:   net.ParseIP("10.0.0.5"),
		User:     "u",
		Database: "u",
	})
	if dec.Method != MethodImplicitReject {
		t.Fatalf("got %s, want implicit-reject", dec.Method)
	}
	if dec.Rule != nil {
		t.Errorf("implicit reject should have nil Rule, got %+v", dec.Rule)
	}
}

func TestAuthenticate(t *testing.T) {
	if err := Authenticate(Decision{Method: MethodTrust}); err != nil {
		t.Errorf("trust returned %v, want nil", err)
	}

	rejErr := Authenticate(Decision{Method: MethodReject, Rule: &Rule{SourceFile: "x", SourceLine: 7}})
	var rej ErrRejected
	if !errors.As(rejErr, &rej) {
		t.Fatalf("reject returned %T (%v), want ErrRejected", rejErr, rejErr)
	}

	if err := Authenticate(Decision{Method: MethodImplicitReject}); err == nil {
		t.Error("implicit reject returned nil error")
	} else if !errors.As(err, &rej) {
		t.Errorf("implicit reject returned %T, want ErrRejected", err)
	}

	unsErr := Authenticate(Decision{Method: MethodSCRAMSHA256})
	var uns ErrMethodUnsupported
	if !errors.As(unsErr, &uns) {
		t.Fatalf("scram returned %T (%v), want ErrMethodUnsupported", unsErr, unsErr)
	}
}

// TestIncludeAndIncludeIfExists covers the file-system include path.
func TestIncludeAndIncludeIfExists(t *testing.T) {
	dir := t.TempDir()
	main := filepath.Join(dir, "main.conf")
	included := filepath.Join(dir, "extra.conf")
	if err := os.WriteFile(included, []byte("host all bob 127.0.0.1/32 reject\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	body := "" +
		"host all all 127.0.0.1/32 trust\n" +
		"include extra.conf\n" +
		"include_if_exists missing.conf\n"
	if err := os.WriteFile(main, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	rs, err := ParseHBAFile(main)
	if err != nil {
		t.Fatal(err)
	}
	if len(rs.Rules) != 2 {
		t.Fatalf("got %d rules, want 2 (include_if_exists must not error)", len(rs.Rules))
	}
	if rs.Rules[1].User[0] != "bob" {
		t.Errorf("rule 1 user = %v, want [bob]", rs.Rules[1].User)
	}
	if rs.Rules[1].Method != MethodReject {
		t.Errorf("rule 1 method = %s, want reject", rs.Rules[1].Method)
	}
}

func TestIncludeCycle(t *testing.T) {
	dir := t.TempDir()
	a := filepath.Join(dir, "a.conf")
	b := filepath.Join(dir, "b.conf")
	if err := os.WriteFile(a, []byte("include b.conf\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(b, []byte("include a.conf\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := ParseHBAFile(a); err == nil {
		t.Fatal("expected cycle error")
	}
}
