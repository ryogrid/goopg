package misc

import "testing"

// Tests for M0146-0155: the GUC nest level a routine's SET clauses open
// (fmgr_security_definer → NewGUCNestLevel, ProcessGUCArray with
// GUC_ACTION_SAVE, AtEOXact_GUC on return). PG oracle:
// postgres/src/backend/utils/misc/guc.c push_old_value / AtEOXact_GUC.

func saveLevelEff(t *testing.T, sess *SessionRegistry, name string) string {
	t.Helper()
	_, eff, _ := sess.GetDisplay(name)
	return eff
}

// TestSaveLevelRestoresClauseValue: the clause value holds inside the level,
// and closing the level restores the prior value — including a SET LOCAL
// made before the call, and a SET LOCAL made inside it (GUC_SAVE stays SAVE).
func TestSaveLevelRestoresClauseValue(t *testing.T) {
	sess := NewSessionRegistry(BuildDefaultRegistry())
	if err := sess.Set("work_mem", "4MB", false); err != nil {
		t.Fatal(err)
	}
	if err := sess.Set("search_path", "a", true); err != nil {
		t.Fatal(err)
	}
	level, err := sess.PushSaveLevel([]string{"work_mem=2MB", "search_path=s1"})
	if err != nil {
		t.Fatal(err)
	}
	if got := saveLevelEff(t, sess, "work_mem"); got != "2MB" {
		t.Fatalf("inside: work_mem = %q, want 2MB", got)
	}
	if got := saveLevelEff(t, sess, "search_path"); got != "s1" {
		t.Fatalf("inside: search_path = %q, want s1", got)
	}
	if err := sess.Set("work_mem", "5MB", true); err != nil {
		t.Fatal(err)
	}
	sess.PopSaveLevel(level)
	if got := saveLevelEff(t, sess, "work_mem"); got != "4MB" {
		t.Errorf("after: work_mem = %q, want 4MB (SET LOCAL inside is undone)", got)
	}
	if got := saveLevelEff(t, sess, "search_path"); got != "a" {
		t.Errorf("after: search_path = %q, want a (the caller's SET LOCAL)", got)
	}
}

// TestSaveLevelPlainSetOverrides: a plain SET or RESET inside the level turns
// the saved entry into GUC_SET, which survives the level — and, merged down,
// every enclosing level that saved the same variable.
func TestSaveLevelPlainSetOverrides(t *testing.T) {
	sess := NewSessionRegistry(BuildDefaultRegistry())
	if err := sess.Set("work_mem", "4MB", false); err != nil {
		t.Fatal(err)
	}
	outer, err := sess.PushSaveLevel([]string{"work_mem=9MB"})
	if err != nil {
		t.Fatal(err)
	}
	inner, err := sess.PushSaveLevel([]string{"work_mem=8MB"})
	if err != nil {
		t.Fatal(err)
	}
	if err := sess.Set("work_mem", "6MB", false); err != nil {
		t.Fatal(err)
	}
	sess.PopSaveLevel(inner)
	sess.PopSaveLevel(outer)
	if got := saveLevelEff(t, sess, "work_mem"); got != "6MB" {
		t.Errorf("work_mem = %q, want 6MB (plain SET outlives both levels)", got)
	}

	level, err := sess.PushSaveLevel([]string{"statement_timeout=7s"})
	if err != nil {
		t.Fatal(err)
	}
	if err := sess.Reset("statement_timeout"); err != nil {
		t.Fatal(err)
	}
	if err := sess.Set("work_mem", "1MB", true); err != nil {
		t.Fatal(err)
	}
	sess.PopSaveLevel(level)
	if got := saveLevelEff(t, sess, "statement_timeout"); got != "0" {
		t.Errorf("statement_timeout = %q, want 0 (RESET inside is a SET)", got)
	}
	// A variable the level did not save behaves as if there were no level:
	// SET LOCAL lasts to the end of the transaction.
	if got := saveLevelEff(t, sess, "work_mem"); got != "1MB" {
		t.Errorf("work_mem = %q, want 1MB (unsaved SET LOCAL merges down)", got)
	}
}

// TestSaveLevelNestedRestoresEachLevel: an inner level wins inside, and each
// level restores its own prior value.
func TestSaveLevelNestedRestoresEachLevel(t *testing.T) {
	sess := NewSessionRegistry(BuildDefaultRegistry())
	if err := sess.Set("work_mem", "4MB", false); err != nil {
		t.Fatal(err)
	}
	outer, _ := sess.PushSaveLevel([]string{"work_mem=9MB"})
	inner, _ := sess.PushSaveLevel([]string{"work_mem=8MB"})
	if got := saveLevelEff(t, sess, "work_mem"); got != "8MB" {
		t.Fatalf("inner = %q, want 8MB", got)
	}
	sess.PopSaveLevel(inner)
	if got := saveLevelEff(t, sess, "work_mem"); got != "9MB" {
		t.Fatalf("after inner = %q, want 9MB", got)
	}
	sess.PopSaveLevel(outer)
	if got := saveLevelEff(t, sess, "work_mem"); got != "4MB" {
		t.Fatalf("after outer = %q, want 4MB", got)
	}
	// Closing an outer level closes a still-open inner one too.
	outer, _ = sess.PushSaveLevel([]string{"work_mem=9MB"})
	_, _ = sess.PushSaveLevel([]string{"work_mem=8MB"})
	sess.PopSaveLevel(outer)
	if got := saveLevelEff(t, sess, "work_mem"); got != "4MB" || len(sess.saveLevels) != 0 {
		t.Fatalf("after outer pop = %q with %d open levels, want 4MB and none", got, len(sess.saveLevels))
	}
}

// TestSaveLevelBadValueClosesLevel: a clause that fails validation fails the
// call and leaves nothing applied.
func TestSaveLevelBadValueClosesLevel(t *testing.T) {
	sess := NewSessionRegistry(BuildDefaultRegistry())
	if err := sess.Set("work_mem", "4MB", false); err != nil {
		t.Fatal(err)
	}
	if _, err := sess.PushSaveLevel([]string{"work_mem=2MB", "work_mem=nonsense"}); err == nil {
		t.Fatal("want a validation error")
	}
	if got := saveLevelEff(t, sess, "work_mem"); got != "4MB" || len(sess.saveLevels) != 0 {
		t.Fatalf("work_mem = %q with %d open levels, want 4MB and none", got, len(sess.saveLevels))
	}
}

// TestSaveLevelParameterStatus: applying and restoring a GUC_REPORT
// variable sends no ParameterStatus (the value at the end of the query is
// the one the client already has), but a SET LOCAL the client was told
// about inside the level is followed by the restored value.
func TestSaveLevelParameterStatus(t *testing.T) {
	sess := NewSessionRegistry(BuildDefaultRegistry())
	var sent []string
	sess.SetReportableHook(func(name, value string) { sent = append(sent, name+"="+value) })
	before := saveLevelEff(t, sess, "DateStyle")

	level, err := sess.PushSaveLevel([]string{"DateStyle=SQL, DMY"})
	if err != nil {
		t.Fatal(err)
	}
	sess.PopSaveLevel(level)
	if len(sent) != 0 {
		t.Fatalf("untouched clause sent %q, want nothing", sent)
	}

	level, _ = sess.PushSaveLevel([]string{"DateStyle=SQL, DMY"})
	if err := sess.Set("DateStyle", "German", true); err != nil {
		t.Fatal(err)
	}
	sess.PopSaveLevel(level)
	if len(sent) != 2 || sent[1] != "DateStyle="+before {
		t.Fatalf("sent %q, want the SET LOCAL value then DateStyle=%s", sent, before)
	}
}
