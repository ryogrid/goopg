package replication_test

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/goopg/goopg/internal/access/transam/xlog"
	"github.com/goopg/goopg/internal/libpq"
)

// TestWalsenderEndsHistoricTimelineAtSwitchPoint: START_REPLICATION on a
// timeline older than the server's streams that timeline's WAL only up to
// the point where the server's history switched off it, then ends the copy
// (CopyDone). After the client's CopyDone it reports the next timeline and
// where it starts (next_tli int8, next_tli_startpos text) and completes
// START_STREAMING — StartReplication / XLogSendPhysical with
// sendTimeLineIsHistoric. A start point already at the switch point gets
// the result set without entering COPY mode, and one past it is refused.
func TestWalsenderEndsHistoricTimelineAtSwitchPoint(t *testing.T) {
	// Fill some WAL first, then pick switch points inside it.
	addr, w, walDir, stop := startTimelinePrimary(t, 3, nil)
	defer stop()
	var ends []uint64
	for i := 0; i < 6; i++ {
		_, end, err := w.Append([]byte(strings.Repeat(fmt.Sprintf("rec%d-", i), 40)))
		if err != nil {
			t.Fatal(err)
		}
		ends = append(ends, end)
	}
	sw1, sw2 := ends[1], ends[3] // timeline 1 ends at sw1, timeline 2 at sw2
	writeHistory(t, walDir, 3, fmt.Sprintf("1\t%X/%X\tno recovery target specified\n2\t%X/%X\tno recovery target specified\n",
		uint32(sw1>>32), uint32(sw1), uint32(sw2>>32), uint32(sw2)))

	for _, tc := range []struct {
		tli     int
		validTo uint64
		nextTLI string
	}{
		{1, sw1, "2"},
		{2, sw2, "3"},
	} {
		t.Run(fmt.Sprintf("timeline %d", tc.tli), func(t *testing.T) {
			conn, r, fw := dialReplication(t, addr)
			defer conn.Close()
			sendQuery(t, fw, fmt.Sprintf("START_REPLICATION 0/0 TIMELINE %d", tc.tli))
			f, err := r.ReadFrame()
			if err != nil || f.Type != libpq.MsgCopyBothResponse {
				t.Fatalf("first frame %c, %v; want CopyBothResponse", f.Type, err)
			}
			var sentTo uint64
			for {
				f, err := r.ReadFrame()
				if err != nil {
					t.Fatalf("stream: %v", err)
				}
				if f.Type == libpq.MsgCopyDone {
					break
				}
				if f.Type != libpq.MsgCopyData {
					t.Fatalf("stream frame %c, want CopyData or CopyDone", f.Type)
				}
				m, kind, err := libpq.DecodeReplicationMessage(f.Payload)
				if err != nil {
					t.Fatal(err)
				}
				if kind != libpq.ReplMsgWALData {
					continue
				}
				d := m.(*libpq.WALDataMessage)
				if d.EndLSN != tc.validTo {
					t.Errorf("walEnd = %X, want the switch point %X", d.EndLSN, tc.validTo)
				}
				sentTo = d.StartLSN + uint64(len(d.WALBytes))
			}
			if sentTo != tc.validTo {
				t.Fatalf("streamed up to %X, want exactly the switch point %X", sentTo, tc.validTo)
			}
			if err := fw.WriteCopyDone(); err != nil {
				t.Fatal(err)
			}
			if err := fw.Flush(); err != nil {
				t.Fatal(err)
			}
			assertNextTimeline(t, readUntilReadyForQuery(t, r), tc.nextTLI, tc.validTo)
		})
	}

	t.Run("start at the switch point", func(t *testing.T) {
		conn, r, fw := dialReplication(t, addr)
		defer conn.Close()
		sendQuery(t, fw, fmt.Sprintf("START_REPLICATION %X/%X TIMELINE 1", uint32(sw1>>32), uint32(sw1)))
		assertNextTimeline(t, readUntilReadyForQuery(t, r), "2", sw1)
	})

	t.Run("start past the switch point", func(t *testing.T) {
		conn, r, fw := dialReplication(t, addr)
		defer conn.Close()
		past := sw2
		sendQuery(t, fw, fmt.Sprintf("START_REPLICATION %X/%X TIMELINE 1", uint32(past>>32), uint32(past)))
		frames := readUntilReadyForQuery(t, r)
		if frames[0].Type != libpq.MsgErrorResponse {
			t.Fatalf("first frame %c, want ErrorResponse", frames[0].Type)
		}
		want := fmt.Sprintf("requested starting point %X/%X on timeline 1 is not in this server's history", uint32(past>>32), uint32(past))
		if !strings.Contains(string(frames[0].Payload), want) || !strings.Contains(string(frames[0].Payload), "C55000") {
			t.Errorf("error = %q, want 55000 %q", frames[0].Payload, want)
		}
	})
}

func writeHistory(t *testing.T, walDir string, tli uint32, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(walDir, xlog.TimelineHistoryFileName(tli)), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

// assertNextTimeline checks StartReplication's closing reply: RowDescription
// (next_tli, next_tli_startpos), one DataRow, CommandComplete
// START_STREAMING, ReadyForQuery.
func assertNextTimeline(t *testing.T, frames []libpq.Frame, nextTLI string, startpos uint64) {
	t.Helper()
	var types []byte
	for _, f := range frames {
		types = append(types, f.Type)
	}
	if string(types) != "TDCZ" {
		t.Fatalf("reply frames %q, want TDCZ", types)
	}
	desc := string(frames[0].Payload)
	if !strings.Contains(desc, "next_tli\x00") || !strings.Contains(desc, "next_tli_startpos\x00") {
		t.Errorf("RowDescription %q lacks next_tli / next_tli_startpos", desc)
	}
	want := fmt.Sprintf("%X/%X", uint32(startpos>>32), uint32(startpos))
	row := string(frames[1].Payload)
	if !strings.Contains(row, nextTLI) || !strings.Contains(row, want) {
		t.Errorf("DataRow %q, want next_tli %s and next_tli_startpos %s", row, nextTLI, want)
	}
	if got := strings.TrimRight(string(frames[2].Payload), "\x00"); got != "START_STREAMING" {
		t.Errorf("CommandComplete %q, want START_STREAMING", got)
	}
}
