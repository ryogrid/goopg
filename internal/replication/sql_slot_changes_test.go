package replication

import "testing"

// TestSQLChangeCollectorEmptyTransaction covers pgoutput's empty-transaction
// rule as pg_logical_slot_get_binary_changes sees it: a BEGIN followed
// directly by COMMIT yields no rows, and a BEGIN is emitted (with its own
// lsn / xid) only once the transaction writes something else. M0149-0051.
func TestSQLChangeCollectorEmptyTransaction(t *testing.T) {
	c := &sqlChangeCollector{}
	write := func(lsn uint64, xid uint32, b byte) {
		t.Helper()
		c.lsn, c.xid = lsn, xid
		if n, err := c.Write([]byte{b, 0}); err != nil || n != 2 {
			t.Fatalf("Write(%c) = %d, %v", b, n, err)
		}
	}
	// Empty transaction: dropped.
	write(10, 700, 'B')
	write(20, 700, 'C')
	// Non-transactional message: one row with xid 0.
	write(30, 0, 'M')
	// Transaction with a message: B, M, C.
	write(40, 701, 'B')
	write(50, 701, 'M')
	write(60, 701, 'C')
	// A trailing BEGIN with nothing after it at the end of decoding.
	write(70, 702, 'B')
	c.flushPendingBegin(false)

	want := []struct {
		lsn uint64
		xid uint32
		tag byte
	}{{30, 0, 'M'}, {40, 701, 'B'}, {50, 701, 'M'}, {60, 701, 'C'}}
	if len(c.rows) != len(want) {
		t.Fatalf("rows = %d, want %d: %+v", len(c.rows), len(want), c.rows)
	}
	for i, w := range want {
		r := c.rows[i]
		if r.LSN != w.lsn || r.XID != w.xid || r.Data[0] != w.tag {
			t.Errorf("row %d = {lsn %d xid %d %c}, want {lsn %d xid %d %c}", i, r.LSN, r.XID, r.Data[0], w.lsn, w.xid, w.tag)
		}
	}
}
