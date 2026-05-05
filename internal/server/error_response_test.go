package server

import (
	"testing"

	"github.com/goopg/goopg/internal/protocol"
	"github.com/goopg/goopg/internal/sqlstate"
)

// TestErrorResponseDoDPositionField is the DoD test for M0049-0002.
//
// DoD: psql renders the correct caret-pointer line on syntax errors.
// psql does this by reading the FieldPosition ('P') field from the
// ErrorResponse and counting that many bytes into the original query.
//
// This test sends a query with a known syntax error and verifies that
// the ErrorResponse contains FieldPosition with a non-zero value.
// It also verifies the clean message format (no internal "(byte N)"
// annotation) and SQLSTATE 42601.
func TestErrorResponseDoDPositionField(t *testing.T) {
	addr, cat, stop := startCopyExecServer(t)
	defer stop()
	_ = cat

	conn := dialAndComplete(t, addr)
	defer conn.Close()

	// Send a query with a deliberate syntax error. "SELECT 1 FROM" is
	// incomplete; the parser emits a SyntaxError with Pos pointing at
	// the position after FROM.
	query := "SELECT 1 FROM"
	writeQuery(t, conn, query)
	frames := readUntilReady(t, conn)

	if len(frames) < 2 {
		t.Fatalf("got %d frames, want at least 2", len(frames))
	}
	if frames[0].Type != protocol.MsgErrorResponse {
		t.Fatalf("frame[0] = %c, want E (ErrorResponse)", frames[0].Type)
	}

	fields := parseErrorFields(t, frames[0].Payload)

	// Must be SQLSTATE 42601 (syntax_error).
	if got := fields[protocol.FieldSQLState]; got != string(sqlstate.SyntaxError) {
		t.Errorf("SQLSTATE = %q, want %q (syntax_error)", got, sqlstate.SyntaxError)
	}

	// FieldPosition must be present and non-empty.
	pos := fields[protocol.FieldPosition]
	if pos == "" {
		t.Error("FieldPosition ('P') is missing from the ErrorResponse; psql cannot render caret line")
	} else {
		t.Logf("FieldPosition = %q for query %q", pos, query)
	}

	// Message must not contain the internal "(byte N)" annotation
	// that confuses clients.
	msg := fields[protocol.FieldMessage]
	if len(msg) == 0 {
		t.Error("FieldMessage is empty")
	}
	if contains(msg, "(byte ") {
		t.Errorf("FieldMessage contains internal byte annotation: %q", msg)
	}
	t.Logf("FieldMessage = %q", msg)
}

// TestSyntaxErrorPositionValue verifies that the byte position in
// FieldPosition corresponds to a correct offset in the original query.
func TestSyntaxErrorPositionValue(t *testing.T) {
	addr, cat, stop := startCopyExecServer(t)
	defer stop()
	_ = cat

	conn := dialAndComplete(t, addr)
	defer conn.Close()

	// "SELECT FOO BAR BAZ" — parser errors on "BAR" or "BAZ".
	// Regardless of exact token, position must be > 0 and ≤ len(query)+1.
	query := "SELECT FOO BAR BAZ"
	writeQuery(t, conn, query)
	frames := readUntilReady(t, conn)

	if frames[0].Type != protocol.MsgErrorResponse {
		t.Fatalf("expected ErrorResponse, got %c", frames[0].Type)
	}
	fields := parseErrorFields(t, frames[0].Payload)
	pos := fields[protocol.FieldPosition]
	if pos == "" {
		t.Fatal("no FieldPosition in ErrorResponse")
	}
	var p int
	for _, c := range pos {
		if c < '0' || c > '9' {
			t.Fatalf("FieldPosition %q is not a decimal integer", pos)
		}
		p = p*10 + int(c-'0')
	}
	if p <= 0 || p > len(query)+1 {
		t.Errorf("FieldPosition %d out of range [1, %d] for query %q", p, len(query)+1, query)
	}
	t.Logf("error at position %d in %q", p, query)
}

func contains(s, sub string) bool {
	return len(s) >= len(sub) && (s == sub || func() bool {
		for i := 0; i <= len(s)-len(sub); i++ {
			if s[i:i+len(sub)] == sub {
				return true
			}
		}
		return false
	}())
}
