package executor

// bytea_input_error_info_test.go — unit coverage for the bytea branch added to
// pg_input_is_valid/pg_input_error_info (both in expr.go and
// operators_pg_input_error_info.go route through byteaIn, bytea.go). Exercises
// byteaIn directly since that is the single source of truth both call sites
// reuse (Hard-won Rule #2). M0134-0070.

import "testing"

func TestByteaInPgInputErrorInfoCases(t *testing.T) {
	cases := []struct {
		in       string
		wantOK   bool
		wantMsg  string
		wantCode string
	}{
		// Odd number of hex digits.
		{`\xDeAdBeE`, false, "invalid hexadecimal data: odd number of digits", "22023"},
		// Bad hex digit 'x'.
		{`\xDeAdBeEx`, false, `invalid hexadecimal digit: "x"`, "22023"},
		// Traditional-escape-format garbage (no \x prefix, lone backslash before
		// non-octal digits).
		{`foo\99bar`, false, "invalid input syntax for type bytea", "22P02"},
		// Valid hex literal — happy path must not regress.
		{`\x1234`, true, "", ""},
	}
	for _, c := range cases {
		_, err := byteaIn(c.in, 0)
		if c.wantOK {
			if err != nil {
				t.Errorf("byteaIn(%q) unexpected error: %v", c.in, err)
			}
			continue
		}
		if err == nil {
			t.Errorf("byteaIn(%q) expected error, got nil", c.in)
			continue
		}
		ee, ok := err.(*ExecError)
		if !ok {
			t.Errorf("byteaIn(%q) error is not *ExecError: %v", c.in, err)
			continue
		}
		if ee.Message != c.wantMsg {
			t.Errorf("byteaIn(%q) message = %q, want %q", c.in, ee.Message, c.wantMsg)
		}
		if ee.Code != c.wantCode {
			t.Errorf("byteaIn(%q) code = %q, want %q", c.in, ee.Code, c.wantCode)
		}
	}
}
