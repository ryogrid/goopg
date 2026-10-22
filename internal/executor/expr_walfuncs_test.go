package executor

import (
	"path/filepath"
	"testing"

	"github.com/goopg/goopg/internal/access/transam/xlog"
)

// TestPgCurrentWALLSN pins pg_current_wal_lsn / _insert_lsn / _flush_lsn
// (xlogfuncs.c): the insert position is the end of the last appended record,
// the flush position follows FlushUpTo, flush <= write <= insert always holds,
// and a standby raises 55000 as RecoveryInProgress does.
func TestPgCurrentWALLSN(t *testing.T) {
	w, err := xlog.NewWriter(xlog.Config{WALDir: filepath.Join(t.TempDir(), "pg_wal"), PageHeaders: true})
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()
	ctx := &Context{WAL: w}
	lsn := func(name string) uint64 {
		t.Helper()
		d, err := evalPgCurrentWALLSN(name, ctx)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		v, err := parsePgLSN(d.StringValue())
		if err != nil {
			t.Fatalf("%s returned %q: %v", name, d.StringValue(), err)
		}
		return v
	}
	_, end, err := w.Append(xlog.EncodeXactCommit(5))
	if err != nil {
		t.Fatal(err)
	}
	if got := lsn("pg_current_wal_insert_lsn"); got != end {
		t.Errorf("insert lsn %X, want the appended record's end %X", got, end)
	}
	if err := w.FlushUpTo(end); err != nil {
		t.Fatal(err)
	}
	if got := lsn("pg_current_wal_flush_lsn"); got != end {
		t.Errorf("flush lsn after FlushUpTo %X, want %X", got, end)
	}
	if _, _, err := w.Append(xlog.EncodeXactCommit(6)); err != nil {
		t.Fatal(err)
	}
	flush, write, insert := lsn("pg_current_wal_flush_lsn"), lsn("pg_current_wal_lsn"), lsn("pg_current_wal_insert_lsn")
	if flush > write || write > insert || insert <= end {
		t.Errorf("flush %X, write %X, insert %X: want flush <= write <= insert and insert past %X", flush, write, insert, end)
	}

	ctx.IsStandby = true
	_, err = evalPgCurrentWALLSN("pg_current_wal_lsn", ctx)
	ee, ok := err.(*ExecError)
	if !ok || ee.Code != "55000" || ee.Message != "recovery is in progress" ||
		ee.Hint != "WAL control functions cannot be executed during recovery." {
		t.Errorf("on a standby: %v, want 55000 recovery is in progress", err)
	}
}

// TestPgLastWALLSN pins pg_last_wal_receive_lsn / pg_last_wal_replay_lsn
// (xlogfuncs.c): NULL on a server with no recovery positions (a primary that
// never ran recovery, or a standby before streaming starts), the formatted
// LSN otherwise, and allowed during recovery. Both were 42883 before
// (AI-20261021-010544-001's diagnostic needs them).
func TestPgLastWALLSN(t *testing.T) {
	for _, name := range []string{"pg_last_wal_receive_lsn", "pg_last_wal_replay_lsn"} {
		if d := evalPgLastWALLSN(name, &Context{}); !d.IsNull() {
			t.Errorf("%s with no recovery positions = %q, want NULL", name, d.StringValue())
		}
	}
	var receive, replay uint64
	ctx := &Context{IsStandby: true, RecoveryLSNs: func() (uint64, uint64) { return receive, replay }}
	if d := evalPgLastWALLSN("pg_last_wal_receive_lsn", ctx); !d.IsNull() {
		t.Errorf("receive before streaming = %q, want NULL", d.StringValue())
	}
	receive, replay = 0x3020_1C8, 0x300_3100
	if d := evalPgLastWALLSN("pg_last_wal_receive_lsn", ctx); d.StringValue() != "0/30201C8" {
		t.Errorf("receive = %q, want 0/30201C8", d.StringValue())
	}
	if d := evalPgLastWALLSN("pg_last_wal_replay_lsn", ctx); d.StringValue() != "0/3003100" {
		t.Errorf("replay = %q, want 0/3003100", d.StringValue())
	}
}
