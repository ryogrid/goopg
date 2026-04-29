package initdb

import (
	"testing"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/parser"
	"github.com/goopg/goopg/internal/wal"
)

// TestStatReplicationRendersRegisteredSenders confirms the view's
// VirtualRows callback observes the live Senders registry: a row
// appears when a sender registers, the LSN columns reflect the
// post-Advance state, and the row vanishes after Unregister.
func TestStatReplicationRendersRegisteredSenders(t *testing.T) {
	cat := catalog.NewInMemory()
	senders := wal.NewSenders()
	if err := registerStatReplicationView(cat, senders); err != nil {
		t.Fatal(err)
	}
	tbl, ok := cat.LookupTable(parser.ObjectName{Schema: "pg_catalog", Name: "pg_stat_replication"})
	if !ok {
		t.Fatal("view not registered")
	}
	if got := tbl.VirtualRows(); len(got) != 0 {
		t.Fatalf("empty registry must yield 0 rows, got %d", len(got))
	}

	se := senders.Register(wal.SenderState{
		PID:             4242,
		ApplicationName: "standby01",
		SlotName:        "phys_replica",
		ClientAddr:      "10.0.0.5:5432",
	})
	se.SetSentLSN(0x100)
	se.ApplyStandbyStatus(0xF0, 0xE0, 0xD0)

	rows := tbl.VirtualRows()
	if len(rows) != 1 {
		t.Fatalf("rows=%d, want 1", len(rows))
	}
	row := rows[0]
	// Column ordering: pid, usesysid, usename, application_name,
	// client_addr, client_hostname, client_port, backend_start,
	// backend_xmin, state, sent_lsn, write_lsn, flush_lsn,
	// replay_lsn, ...
	checks := []struct {
		idx  int
		col  string
		want string
	}{
		{0, "pid", "4242"},
		{3, "application_name", "standby01"},
		{4, "client_addr", "10.0.0.5:5432"},
		{9, "state", "streaming"},
		{10, "sent_lsn", "0/100"},
		{11, "write_lsn", "0/F0"},
		{12, "flush_lsn", "0/E0"},
		{13, "replay_lsn", "0/D0"},
		{18, "sync_state", "async"},
		{20, "slot_name", "phys_replica"},
	}
	for _, c := range checks {
		if row[c.idx] != c.want {
			t.Errorf("%s = %q, want %q", c.col, row[c.idx], c.want)
		}
	}

	senders.Unregister(se)
	if rows := tbl.VirtualRows(); len(rows) != 0 {
		t.Errorf("after unregister: rows=%d, want 0", len(rows))
	}
}

// TestStatWalReceiverRendersWhenRegistered confirms the
// pg_stat_wal_receiver view returns one row when a receiver is
// registered and zero rows otherwise. The receiver's progress
// updates flow into the snapshot.
func TestStatWalReceiverRendersWhenRegistered(t *testing.T) {
	cat := catalog.NewInMemory()
	receivers := wal.NewReceivers()
	if err := registerStatWalReceiverView(cat, receivers); err != nil {
		t.Fatal(err)
	}
	tbl, ok := cat.LookupTable(parser.ObjectName{Schema: "pg_catalog", Name: "pg_stat_wal_receiver"})
	if !ok {
		t.Fatal("view not registered")
	}
	if rows := tbl.VirtualRows(); len(rows) != 0 {
		t.Fatalf("no receiver: rows=%d, want 0", len(rows))
	}

	rec := receivers.Register(wal.ReceiverState{
		PID:             1234,
		Status:          "streaming",
		ReceiveStartLSN: 0x100,
		SenderHost:      "primary-a:5432",
		SlotName:        "phys_replica",
		Conninfo:        "host=primary-a port=5432",
	})
	rec.SetReceivedLSN(0x200)

	rows := tbl.VirtualRows()
	if len(rows) != 1 {
		t.Fatalf("rows=%d, want 1", len(rows))
	}
	row := rows[0]
	// pid, status, receive_start_lsn, receive_start_tli,
	// written_lsn, flushed_lsn, received_tli, ...
	checks := []struct {
		idx  int
		col  string
		want string
	}{
		{0, "pid", "1234"},
		{1, "status", "streaming"},
		{2, "receive_start_lsn", "0/100"},
		{3, "receive_start_tli", "1"},
		{4, "written_lsn", "0/200"},
		{5, "flushed_lsn", "0/200"},
		{11, "slot_name", "phys_replica"},
		{12, "sender_host", "primary-a:5432"},
		{14, "conninfo", "host=primary-a port=5432"},
	}
	for _, c := range checks {
		if row[c.idx] != c.want {
			t.Errorf("%s = %q, want %q", c.col, row[c.idx], c.want)
		}
	}

	receivers.Unregister(rec)
	if rows := tbl.VirtualRows(); len(rows) != 0 {
		t.Errorf("after unregister: rows=%d, want 0", len(rows))
	}
}

// TestFormatLSN pins the upstream-aligned hex format. Operators
// using `psql -c "SELECT pg_lsn_diff('1/0', sent_lsn) FROM ..."`
// expect the X/X form, not decimal.
func TestFormatLSN(t *testing.T) {
	cases := []struct {
		in   uint64
		want string
	}{
		{0, "0/0"},
		{0x100, "0/100"},
		{0x100000000, "1/0"},
		{0x1_0000_00FF, "1/FF"},
	}
	for _, c := range cases {
		if got := formatLSN(c.in); got != c.want {
			t.Errorf("formatLSN(%d) = %q, want %q", c.in, got, c.want)
		}
	}
}
