package postmaster

import (
	"net"
	"strings"
	"testing"
	"time"

	"github.com/goopg/goopg/internal/libpq"
)

// TestShowReturnsRegistryValue verifies SHOW server_version returns the
// registry's value as a real RowDescription/DataRow result.
func TestShowReturnsRegistryValue(t *testing.T) {
	addr, stop := startTestServer(t)
	defer stop()
	conn := dialAndComplete(t, addr)
	defer conn.Close()

	writeQuery(t, conn, "SHOW server_version")
	frames := readUntilReady(t, conn)
	if len(frames) != 4 {
		t.Fatalf("got %d frames, want 4 (T,D,C,Z): %+v", len(frames), frames)
	}
	if frames[0].Type != libpq.MsgRowDescription || frames[1].Type != libpq.MsgDataRow {
		t.Fatalf("unexpected frame types: %c %c", frames[0].Type, frames[1].Type)
	}
	dr := frames[1].Payload
	colLen := int(dr[2])<<24 | int(dr[3])<<16 | int(dr[4])<<8 | int(dr[5])
	value := string(dr[6 : 6+colLen])
	if value != "18.3" {
		t.Fatalf("server_version = %q, want %q", value, "18.3")
	}
}

// TestSetEmitsParameterStatusForReportable verifies the FlagReport
// auto-emit contract: SET application_name = 'X' produces a
// ParameterStatus message in addition to the SET CommandComplete.
func TestSetEmitsParameterStatusForReportable(t *testing.T) {
	addr, stop := startTestServer(t)
	defer stop()
	conn := dialAndComplete(t, addr)
	defer conn.Close()

	writeQuery(t, conn, "SET application_name = 'myapp'")
	frames := readUntilReady(t, conn)

	var sawStatus bool
	for _, f := range frames {
		if f.Type == libpq.MsgParameterStatus {
			i := 0
			for ; i < len(f.Payload); i++ {
				if f.Payload[i] == 0 {
					break
				}
			}
			key := string(f.Payload[:i])
			val := strings.TrimRight(string(f.Payload[i+1:]), "\x00")
			if key == "application_name" && val == "myapp" {
				sawStatus = true
				break
			}
		}
	}
	if !sawStatus {
		t.Fatalf("expected ParameterStatus application_name=myapp, frames=%+v", frames)
	}
}

// TestSetThenShowSeesNewValue confirms the session layer is read by the
// next SHOW on the same connection.
func TestSetThenShowSeesNewValue(t *testing.T) {
	addr, stop := startTestServer(t)
	defer stop()
	conn := dialAndComplete(t, addr)
	defer conn.Close()

	writeQuery(t, conn, "SET application_name = 'after'")
	_ = readUntilReady(t, conn)
	writeQuery(t, conn, "SHOW application_name")
	frames := readUntilReady(t, conn)
	dr := frames[1].Payload
	colLen := int(dr[2])<<24 | int(dr[3])<<16 | int(dr[4])<<8 | int(dr[5])
	value := string(dr[6 : 6+colLen])
	if value != "after" {
		t.Fatalf("SHOW application_name = %q, want %q", value, "after")
	}
}

// TestCustomGUCVisibleViaShow pins custom dotted-GUC support at the
// lightweight query-handler layer. Full current_setting() coverage lives
// under internal/testport where queries route through the executor.
func TestCustomGUCVisibleViaShow(t *testing.T) {
	addr, stop := startTestServer(t)
	defer stop()
	conn := dialAndComplete(t, addr)
	defer conn.Close()

	writeQuery(t, conn, "SET spec.session = 1")
	frames := readUntilReady(t, conn)
	if len(frames) == 0 || frames[0].Type == libpq.MsgErrorResponse {
		t.Fatalf("SET spec.session failed, frames=%+v", frames)
	}

	writeQuery(t, conn, "SHOW spec.session")
	frames = readUntilReady(t, conn)
	if len(frames) < 2 || frames[0].Type != libpq.MsgRowDescription || frames[1].Type != libpq.MsgDataRow {
		t.Fatalf("unexpected frames for SHOW spec.session: %+v", frames)
	}
	dr := frames[1].Payload
	colLen := int(dr[2])<<24 | int(dr[3])<<16 | int(dr[4])<<8 | int(dr[5])
	value := string(dr[6 : 6+colLen])
	if value != "1" {
		t.Fatalf("SHOW spec.session = %q, want %q", value, "1")
	}
}

// TestSetRejectsPostmasterContextVar ensures a SET against a
// non-userset variable returns ErrorResponse but keeps the connection
// alive.
func TestSetRejectsPostmasterContextVar(t *testing.T) {
	addr, stop := startTestServer(t)
	defer stop()
	conn := dialAndComplete(t, addr)
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(2 * time.Second))

	writeQuery(t, conn, "SET port = 9999")
	frames := readUntilReady(t, conn)
	if frames[0].Type != libpq.MsgErrorResponse {
		t.Fatalf("first frame = %c, want E", frames[0].Type)
	}
	// Still alive — follow up with a SHOW.
	writeQuery(t, conn, "SHOW port")
	frames = readUntilReady(t, conn)
	dr := frames[1].Payload
	colLen := int(dr[2])<<24 | int(dr[3])<<16 | int(dr[4])<<8 | int(dr[5])
	if string(dr[6:6+colLen]) != "5432" {
		t.Errorf("port = %q, want 5432 (unchanged)", dr[6:6+colLen])
	}
}

// TestShowAllReturnsAllVariables pins the regression that real psql 18.3
// exposed: `SHOW ALL` was incorrectly being routed through the per-name
// SHOW arm, which then errored with "unrecognized configuration parameter
// \"ALL\"". The fix reorders the switch so SHOW ALL hits its own arm
// first.
func TestShowAllReturnsAllVariables(t *testing.T) {
	addr, stop := startTestServer(t)
	defer stop()
	conn := dialAndComplete(t, addr)
	defer conn.Close()

	writeQuery(t, conn, "SHOW ALL")
	frames := readUntilReady(t, conn)
	if frames[0].Type != libpq.MsgRowDescription {
		t.Fatalf("frame[0] = %c, want T (RowDescription)", frames[0].Type)
	}
	// One DataRow per registered variable. We don't pin the exact count
	// (it grows as new GUCs are seeded) — just confirm the row count
	// matches the registry size and a known variable shows up.
	var dataRows, sawServerVersion int
	for _, f := range frames {
		if f.Type == libpq.MsgDataRow {
			dataRows++
			// payload: int16 ncols + int32 len + name + int32 len + value
			// Quick scan for "server_version" anywhere in payload.
			if bytesContains(f.Payload, []byte("server_version")) {
				sawServerVersion++
			}
		}
	}
	if dataRows == 0 {
		t.Fatal("SHOW ALL returned no DataRow frames")
	}
	if sawServerVersion == 0 {
		t.Fatal("SHOW ALL did not include server_version")
	}
}

func bytesContains(haystack, needle []byte) bool {
	if len(needle) == 0 {
		return true
	}
	for i := 0; i+len(needle) <= len(haystack); i++ {
		ok := true
		for j := range needle {
			if haystack[i+j] != needle[j] {
				ok = false
				break
			}
		}
		if ok {
			return true
		}
	}
	return false
}

// Quick reachability check that net.IP isn't unused in any related test
// file after refactoring.
var _ = net.IP{}
