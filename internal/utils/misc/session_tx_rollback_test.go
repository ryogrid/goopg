package misc

import "testing"

// Tests for 0134-0001 P6/S15: PostgreSQL's rule that a plain (non-LOCAL) SET
// inside an explicit transaction is unwound on ROLLBACK, exactly as SET LOCAL
// already was — see docs/design/0134-0001-p6-guc-transaction-rollback.md and
// PG oracle postgres/src/backend/utils/misc/guc.c (AtEOXact_GUC).

// TestSessionPlainSetRevertsOnRollback covers the base case: SET, then
// ROLLBACK, restores the pre-BEGIN value.
func TestSessionPlainSetRevertsOnRollback(t *testing.T) {
	r := BuildDefaultRegistry()
	sess := NewSessionRegistry(r)

	if err := sess.Set("application_name", "before", false); err != nil {
		t.Fatal(err)
	}
	sess.BeginTransaction()
	if err := sess.Set("application_name", "in-txn", false); err != nil {
		t.Fatal(err)
	}
	if _, eff, _ := sess.Get("application_name"); eff != "in-txn" {
		t.Fatalf("in txn = %q, want in-txn", eff)
	}
	sess.EndTransaction(false)
	if _, eff, _ := sess.Get("application_name"); eff != "before" {
		t.Fatalf("after ROLLBACK = %q, want before (pre-BEGIN value)", eff)
	}
	if sess.inTx {
		t.Fatal("inTx must be false after EndTransaction")
	}
}

// TestSessionPlainSetKeptOnCommit covers the base case: SET, then COMMIT,
// keeps the new value.
func TestSessionPlainSetKeptOnCommit(t *testing.T) {
	r := BuildDefaultRegistry()
	sess := NewSessionRegistry(r)

	if err := sess.Set("application_name", "before", false); err != nil {
		t.Fatal(err)
	}
	sess.BeginTransaction()
	if err := sess.Set("application_name", "in-txn", false); err != nil {
		t.Fatal(err)
	}
	sess.EndTransaction(true)
	if _, eff, _ := sess.Get("application_name"); eff != "in-txn" {
		t.Fatalf("after COMMIT = %q, want in-txn (must persist)", eff)
	}
}

// TestSessionPlainSetOutsideTxnIsPermanent is a regression guard: a plain SET
// issued with no enclosing transaction is not journalled and is never
// reverted by any later EndTransaction call.
func TestSessionPlainSetOutsideTxnIsPermanent(t *testing.T) {
	r := BuildDefaultRegistry()
	sess := NewSessionRegistry(r)

	if err := sess.Set("application_name", "standalone", false); err != nil {
		t.Fatal(err)
	}
	if _, eff, _ := sess.Get("application_name"); eff != "standalone" {
		t.Fatalf("before txn = %q, want standalone", eff)
	}
	// An EndTransaction(false) with nothing journalled (never called
	// BeginTransaction) must not disturb the standalone SET.
	sess.EndTransaction(false)
	if _, eff, _ := sess.Get("application_name"); eff != "standalone" {
		t.Fatalf("after stray EndTransaction(false) = %q, want standalone (unaffected)", eff)
	}
}

// TestSessionPlainSetNoPriorEntryDeletedOnRollback is the nil-pointer case:
// a variable with no session-layer entry before the transaction must be
// DELETED on rollback (falling through to the boot/global value), not left
// holding an empty string.
func TestSessionPlainSetNoPriorEntryDeletedOnRollback(t *testing.T) {
	r := BuildDefaultRegistry()
	sess := NewSessionRegistry(r)

	// application_name has BootVal "" and no session-layer entry yet.
	v, before, _ := sess.Get("application_name")
	if before != v.Value {
		t.Fatalf("precondition: application_name effective=%q, want boot value %q", before, v.Value)
	}
	if _, present := sess.session["application_name"]; present {
		t.Fatalf("precondition: application_name must have no session-layer entry")
	}

	sess.BeginTransaction()
	if err := sess.Set("application_name", "in-txn", false); err != nil {
		t.Fatal(err)
	}
	sess.EndTransaction(false)

	if _, present := sess.session["application_name"]; present {
		t.Fatalf("after ROLLBACK: application_name still has a session-layer entry (want deleted)")
	}
	if _, eff, _ := sess.Get("application_name"); eff != v.Value {
		t.Fatalf("after ROLLBACK = %q, want boot/global value %q", eff, v.Value)
	}
}

// TestSessionTwoSetsInOneTxnRestorePreBeginValue covers the "journal once
// per key" rule: two SETs of the same variable inside one transaction, then
// ROLLBACK, must restore the pre-BEGIN value — not the first in-transaction
// value.
func TestSessionTwoSetsInOneTxnRestorePreBeginValue(t *testing.T) {
	r := BuildDefaultRegistry()
	sess := NewSessionRegistry(r)

	if err := sess.Set("application_name", "pre-begin", false); err != nil {
		t.Fatal(err)
	}
	sess.BeginTransaction()
	if err := sess.Set("application_name", "first", false); err != nil {
		t.Fatal(err)
	}
	if err := sess.Set("application_name", "second", false); err != nil {
		t.Fatal(err)
	}
	sess.EndTransaction(false)
	if _, eff, _ := sess.Get("application_name"); eff != "pre-begin" {
		t.Fatalf("after ROLLBACK = %q, want pre-begin (not the intermediate first/second)", eff)
	}
}

// TestSessionSetLocalUnaffectedByRollbackJournal pins the sibling contract:
// SET LOCAL behaviour (dropped at both COMMIT and ROLLBACK) is unchanged by
// the new plain-SET undo journal.
func TestSessionSetLocalUnaffectedByRollbackJournal(t *testing.T) {
	r := BuildDefaultRegistry()

	t.Run("commit", func(t *testing.T) {
		sess := NewSessionRegistry(r)
		if err := sess.Set("application_name", "session", false); err != nil {
			t.Fatal(err)
		}
		sess.BeginTransaction()
		if err := sess.Set("application_name", "local", true); err != nil {
			t.Fatal(err)
		}
		if _, eff, _ := sess.Get("application_name"); eff != "local" {
			t.Fatalf("in txn = %q, want local", eff)
		}
		sess.EndTransaction(true)
		if _, eff, _ := sess.Get("application_name"); eff != "session" {
			t.Fatalf("after COMMIT = %q, want session (LOCAL always drops)", eff)
		}
	})

	t.Run("rollback", func(t *testing.T) {
		sess := NewSessionRegistry(r)
		if err := sess.Set("application_name", "session", false); err != nil {
			t.Fatal(err)
		}
		sess.BeginTransaction()
		if err := sess.Set("application_name", "local", true); err != nil {
			t.Fatal(err)
		}
		sess.EndTransaction(false)
		if _, eff, _ := sess.Get("application_name"); eff != "session" {
			t.Fatalf("after ROLLBACK = %q, want session (LOCAL always drops)", eff)
		}
	})
}

// TestSessionResetInTxnRevertedByRollback: RESET inside a transaction is
// itself journalled (Reset calls snapshotPrior) and is reverted by ROLLBACK.
func TestSessionResetInTxnRevertedByRollback(t *testing.T) {
	r := BuildDefaultRegistry()
	sess := NewSessionRegistry(r)

	if err := sess.Set("application_name", "before-reset", false); err != nil {
		t.Fatal(err)
	}
	sess.BeginTransaction()
	if err := sess.Reset("application_name"); err != nil {
		t.Fatal(err)
	}
	v, _, _ := sess.Get("application_name")
	if _, eff, _ := sess.Get("application_name"); eff != v.Value {
		t.Fatalf("in txn after RESET = %q, want boot value %q", eff, v.Value)
	}
	sess.EndTransaction(false)
	if _, eff, _ := sess.Get("application_name"); eff != "before-reset" {
		t.Fatalf("after ROLLBACK of in-txn RESET = %q, want before-reset", eff)
	}
}

// TestSessionRollbackFiresOnReportableChange: a FlagReport variable
// (application_name/DateStyle are both GUC_REPORT upstream) fires
// onReportableChange with the restored value on rollback, so the client's
// ParameterStatus view does not desync from a rolled-back SET.
func TestSessionRollbackFiresOnReportableChange(t *testing.T) {
	r := BuildDefaultRegistry()
	sess := NewSessionRegistry(r)

	var reported []struct{ name, value string }
	if err := sess.Set("DateStyle", "ISO, MDY", false); err != nil {
		t.Fatal(err)
	}
	sess.SetReportableHook(func(name, value string) {
		reported = append(reported, struct{ name, value string }{name, value})
	})

	sess.BeginTransaction()
	if err := sess.Set("DateStyle", "Postgres, MDY", false); err != nil {
		t.Fatal(err)
	}
	reported = nil // only care about the rollback-triggered report
	sess.EndTransaction(false)

	if len(reported) == 0 {
		t.Fatal("EndTransaction(false) did not fire onReportableChange for a restored FlagReport variable")
	}
	found := false
	for _, r := range reported {
		if r.name == "DateStyle" && r.value == "ISO, MDY" {
			found = true
		}
	}
	if !found {
		t.Fatalf("onReportableChange reports = %+v, want a DateStyle=%q entry", reported, "ISO, MDY")
	}
	if _, eff, _ := sess.Get("DateStyle"); eff != "ISO, MDY" {
		t.Fatalf("after ROLLBACK DateStyle = %q, want ISO, MDY", eff)
	}
}
