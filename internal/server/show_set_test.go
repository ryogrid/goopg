package server

import (
	"net"
	"strings"
	"testing"
	"time"

	"github.com/goopg/goopg/internal/protocol"
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
	if frames[0].Type != protocol.MsgRowDescription || frames[1].Type != protocol.MsgDataRow {
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
		if f.Type == protocol.MsgParameterStatus {
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
	if frames[0].Type != protocol.MsgErrorResponse {
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

// Quick reachability check that net.IP isn't unused in any related test
// file after refactoring.
var _ = net.IP{}
