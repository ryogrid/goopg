package misc

import "testing"

// TestSessionImplicitTransaction pins AtEOXact_GUC for transactions the
// client never opened: an autocommit message's SET LOCAL / set_config(...,
// true) value ends with the message, an aborted message undoes its plain
// SETs, a BEGIN inside the message carries both into the explicit block,
// and InTransactionBlock is IsTransactionBlock() (explicit BEGIN or a
// multi-statement message's implicit block).
func TestSessionImplicitTransaction(t *testing.T) {
	show := func(s *SessionRegistry) string {
		_, v, _ := s.GetDisplay("work_mem")
		return v
	}
	s := NewSessionRegistry(BuildDefaultRegistry())
	base := show(s)

	if s.InTransactionBlock() {
		t.Fatal("fresh session reports a transaction block")
	}
	// Single-statement message: no block; the local value ends with it.
	s.BeginImplicitTransaction(false)
	if s.InTransactionBlock() {
		t.Error("single-statement message reported as a block")
	}
	if err := s.Set("work_mem", "6MB", true); err != nil {
		t.Fatal(err)
	}
	if got := show(s); got != "6MB" {
		t.Errorf("local value inside the message = %q, want 6MB", got)
	}
	s.EndImplicitTransaction(true)
	if got := show(s); got != base {
		t.Errorf("local value survived the message: %q, want %q", got, base)
	}

	// Multi-statement message: an implicit block.
	s.BeginImplicitTransaction(true)
	if !s.InTransactionBlock() {
		t.Error("multi-statement message not reported as a block")
	}
	s.EndImplicitTransaction(true)

	// An aborted message undoes its plain SET; a committed one keeps it.
	s.BeginImplicitTransaction(true)
	_ = s.Set("work_mem", "7MB", false)
	s.EndImplicitTransaction(false)
	if got := show(s); got != base {
		t.Errorf("plain SET survived an aborted message: %q", got)
	}
	s.BeginImplicitTransaction(true)
	_ = s.Set("work_mem", "8MB", false)
	s.EndImplicitTransaction(true)
	if got := show(s); got != "8MB" {
		t.Errorf("plain SET lost by a committed message: %q", got)
	}

	// SET LOCAL, then BEGIN in the same message: the block inherits the
	// local value and the journal, and the message end leaves both alone.
	s.BeginImplicitTransaction(true)
	_ = s.Set("work_mem", "11MB", true)
	_ = s.Set("work_mem", "12MB", false)
	_ = s.Set("work_mem", "13MB", true)
	s.BeginTransaction()
	s.EndImplicitTransaction(false)
	if got := show(s); got != "13MB" || !s.InTransactionBlock() {
		t.Errorf("after the message, inside the promoted block: %q block=%v", got, s.InTransactionBlock())
	}
	s.EndTransaction(false) // ROLLBACK undoes the pre-BEGIN plain SET too
	if got := show(s); got != "8MB" {
		t.Errorf("after ROLLBACK of the promoted block = %q, want 8MB", got)
	}

	// CheckSet validates without storing.
	if err := s.CheckSet("work_mem", "bogus"); err == nil {
		t.Error("CheckSet accepted an invalid value")
	}
	if err := s.CheckSet("work_mem", "1MB"); err != nil || show(s) != "8MB" {
		t.Errorf("CheckSet: err=%v, value now %q", err, show(s))
	}
}
