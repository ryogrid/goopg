package executor

import "testing"

// TestSequencePageStateShowsPage: after the counter is restored from the
// sequence page (startup reload, or a standby replaying XLOG_SEQ_LOG),
// SELECT * FROM seq shows the page — the logged-ahead last_value with its
// log_cnt — as PG's read_seq_tuple does (001_stream_rep expects 33|0|t on a
// standby after one nextval on the primary). This server's own nextval
// then goes back to the live counter. M0149-0077a.
func TestSequencePageStateShowsPage(t *testing.T) {
	const name, db = "m77a_seq", uint32(5)
	RegisterSequence(name, 1, 1, 1, 1<<62, false, db)
	defer DropSequence(name, db)
	if !SetSequencePageState(name, 33, 0, true, db) {
		t.Fatal("sequence not registered")
	}
	if lv, lc, called, ok := SequenceRowData(name, db); !ok || lv != 33 || lc != 0 || !called {
		t.Fatalf("row = %d|%d|%v %v, want 33|0|t", lv, lc, called, ok)
	}
	s := LookupSequence(name, db)
	if v, err := s.nextVal(); err != nil || v != 34 {
		t.Fatalf("nextval after the page state = %d, %v; want 34", v, err)
	}
	if lv, lc, _, _ := SequenceRowData(name, db); lv != 34 || lc != 32 {
		t.Fatalf("after nextval: %d|%d, want 34|32", lv, lc)
	}
	if SetSequencePageState("m77a_missing", 1, 0, true, db) {
		t.Fatal("an unregistered sequence was set")
	}
}
