package postmaster

import (
	"net"
	"strings"
	"testing"
	"time"

	"github.com/goopg/goopg/internal/libpq"
)

// TestReplicationStartupParamParsing pins the accepted spellings against
// upstream parse_bool (postgres/src/backend/utils/adt/bool.c:37), which
// compares the input against the full word with pg_strncasecmp over the
// INPUT's length — so every non-empty prefix is valid. Before
// review/260831-2 CP-5 every value outside {"", "0", "false", "FALSE",
// "False"} was read as "replication requested", so `off` and `no` took the
// walsender route and `bogus` was accepted silently.
func TestReplicationStartupParamParsing(t *testing.T) {
	cases := []struct {
		value   string
		isRepl  bool
		isValid bool
	}{
		{"", false, true},
		{"database", true, true},
		{"true", true, true}, {"TRUE", true, true}, {"t", true, true}, {"tr", true, true},
		{"on", true, true}, {"yes", true, true}, {"y", true, true}, {"1", true, true},
		{"false", false, true}, {"False", false, true}, {"f", false, true},
		{"off", false, true}, {"of", false, true}, {"no", false, true}, {"n", false, true},
		{"0", false, true},
		{"bogus", false, false}, {"0x0", false, false}, {"truex", false, false},
		{"databas", false, false}, {"2", false, false},
	}
	for _, tc := range cases {
		gotRepl, gotOK := isReplicationStartupParam(tc.value)
		if gotOK != tc.isValid || gotRepl != tc.isRepl {
			t.Errorf("isReplicationStartupParam(%q) = (%v, %v), want (%v, %v)",
				tc.value, gotRepl, gotOK, tc.isRepl, tc.isValid)
		}
	}
}

// TestReplicationStartupParamInvalidIsFatal is the wire half: PG 18.3 answers
// `replication=bogus` in the StartupMessage with FATAL 22023 `invalid value
// for parameter "replication": "bogus"` plus the valid-values HINT, and closes
// the connection. goopg used to complete the handshake instead.
func TestReplicationStartupParamInvalidIsFatal(t *testing.T) {
	addr, stop := startTestServer(t)
	defer stop()

	conn, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(2 * time.Second))
	writeStartupPacket(t, conn, map[string]string{"user": "postgres", "replication": "bogus"})

	r := libpq.NewFrameReader(conn)
	f, err := r.ReadFrame()
	if err != nil {
		t.Fatalf("read response: %v", err)
	}
	if f.Type != libpq.MsgErrorResponse {
		t.Fatalf("got message type %q, want ErrorResponse", f.Type)
	}
	body := string(f.Payload)
	for _, want := range []string{"22023", `invalid value for parameter "replication"`, "bogus", "Valid values are"} {
		if !strings.Contains(body, want) {
			t.Errorf("ErrorResponse %q does not contain %q", body, want)
		}
	}
}
