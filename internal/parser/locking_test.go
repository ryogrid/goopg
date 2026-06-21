package parser

import "testing"

// TestParseSelectForUpdateBasic — the headline shape: a bare `FOR
// UPDATE` tail on a SELECT. Pins (1) parse success, (2) Strength
// ForUpdate, (3) empty Targets (lock applies to all FROM-clause
// rels), (4) default WaitBlock policy.
func TestParseSelectForUpdateBasic(t *testing.T) {
	stmts, err := Parse("SELECT * FROM pgbench_accounts WHERE aid = 1 FOR UPDATE")
	if err != nil {
		t.Fatal(err)
	}
	s := stmts[0].(*SelectStmt)
	if len(s.Locking) != 1 {
		t.Fatalf("Locking len = %d, want 1", len(s.Locking))
	}
	lc := s.Locking[0]
	if lc.Strength != LockStrengthForUpdate {
		t.Errorf("Strength = %v, want LockStrengthForUpdate", lc.Strength)
	}
	if len(lc.Targets) != 0 {
		t.Errorf("Targets = %v, want empty", lc.Targets)
	}
	if lc.WaitPolicy != LockWaitBlock {
		t.Errorf("WaitPolicy = %v, want LockWaitBlock", lc.WaitPolicy)
	}
}

// TestParseSelectForShare — read-intent variant. Pins
// LockStrengthForShare so future analyzer / executor stages can
// distinguish read- and write-intent locks without re-parsing.
func TestParseSelectForShare(t *testing.T) {
	stmts, err := Parse("SELECT * FROM pgbench_accounts FOR SHARE")
	if err != nil {
		t.Fatal(err)
	}
	s := stmts[0].(*SelectStmt)
	if s.Locking[0].Strength != LockStrengthForShare {
		t.Errorf("Strength = %v, want LockStrengthForShare", s.Locking[0].Strength)
	}
}

// TestParseSelectForUpdateOf — the `OF table_name [, ...]` shape.
// Pins (1) Targets are captured in source order, (2) the alias /
// table-name resolution is left to the analyzer (parser stays
// purely syntactic).
func TestParseSelectForUpdateOf(t *testing.T) {
	stmts, err := Parse("SELECT * FROM pgbench_accounts a, pgbench_history h FOR UPDATE OF a, h")
	if err != nil {
		t.Fatal(err)
	}
	s := stmts[0].(*SelectStmt)
	got := s.Locking[0].Targets
	want := []string{"a", "h"}
	if len(got) != len(want) {
		t.Fatalf("Targets = %v, want %v", got, want)
	}
	for i, name := range want {
		if got[i] != name {
			t.Errorf("Targets[%d] = %q, want %q", i, got[i], name)
		}
	}
}

// TestParseSelectForUpdateNoWait — the `NOWAIT` modifier. Stage A
// scope: parser accepts it; analyzer/executor honour it later.
func TestParseSelectForUpdateNoWait(t *testing.T) {
	stmts, err := Parse("SELECT * FROM pgbench_accounts FOR UPDATE NOWAIT")
	if err != nil {
		t.Fatal(err)
	}
	if s := stmts[0].(*SelectStmt); s.Locking[0].WaitPolicy != LockWaitNoWait {
		t.Errorf("WaitPolicy = %v, want LockWaitNoWait", s.Locking[0].WaitPolicy)
	}
}

// TestParseSelectForUpdateSkipLocked — the `SKIP LOCKED` modifier
// (two keywords; tests pin that both are consumed and the policy
// enum lands correctly).
func TestParseSelectForUpdateSkipLocked(t *testing.T) {
	stmts, err := Parse("SELECT * FROM pgbench_accounts FOR UPDATE SKIP LOCKED")
	if err != nil {
		t.Fatal(err)
	}
	if s := stmts[0].(*SelectStmt); s.Locking[0].WaitPolicy != LockWaitSkipLocked {
		t.Errorf("WaitPolicy = %v, want LockWaitSkipLocked", s.Locking[0].WaitPolicy)
	}
}

// TestParseSelectForUpdateMultiClause — upstream allows multiple
// locking clauses per SELECT (e.g. `FOR UPDATE OF a NOWAIT FOR
// SHARE OF b`). Pins that the parser collects them in source
// order so the planner can apply the per-target wait policies
// independently.
func TestParseSelectForUpdateMultiClause(t *testing.T) {
	stmts, err := Parse("SELECT * FROM pgbench_accounts a, pgbench_history h FOR UPDATE OF a NOWAIT FOR SHARE OF h")
	if err != nil {
		t.Fatal(err)
	}
	s := stmts[0].(*SelectStmt)
	if len(s.Locking) != 2 {
		t.Fatalf("Locking len = %d, want 2", len(s.Locking))
	}
	if s.Locking[0].Strength != LockStrengthForUpdate || s.Locking[0].WaitPolicy != LockWaitNoWait {
		t.Errorf("first clause = %+v", s.Locking[0])
	}
	if s.Locking[1].Strength != LockStrengthForShare || s.Locking[1].WaitPolicy != LockWaitBlock {
		t.Errorf("second clause = %+v", s.Locking[1])
	}
}

// TestParseSelectForUpdateAfterLimit — locking clauses must come
// AFTER LIMIT/OFFSET/FETCH per upstream's grammar, mirroring
// production-emitted ordering. ORMs emit `... LIMIT 10 FOR UPDATE`.
func TestParseSelectForUpdateAfterLimit(t *testing.T) {
	stmts, err := Parse("SELECT * FROM pgbench_accounts ORDER BY aid LIMIT 10 FOR UPDATE")
	if err != nil {
		t.Fatal(err)
	}
	s := stmts[0].(*SelectStmt)
	if s.Limit == nil {
		t.Fatal("Limit nil — should have parsed before locking clause")
	}
	if len(s.Locking) != 1 {
		t.Fatalf("Locking len = %d, want 1", len(s.Locking))
	}
}

// TestParseSelectForUpdateBeforeLimit — PostgreSQL's grammar permits the
// select_limit to FOLLOW the for_locking_clause as well:
// `... ORDER BY id FOR UPDATE [SKIP LOCKED | NOWAIT] LIMIT n`. The upstream
// skip-locked / nowait isolation specs emit exactly this ordering. Before
// M0118-0003 the parser rejected the trailing LIMIT with a syntax error.
func TestParseSelectForUpdateBeforeLimit(t *testing.T) {
	cases := []struct {
		sql        string
		wantPolicy LockWaitPolicy
	}{
		{"SELECT * FROM queue ORDER BY id FOR UPDATE LIMIT 1", LockWaitBlock},
		{"SELECT * FROM queue ORDER BY id FOR UPDATE SKIP LOCKED LIMIT 1", LockWaitSkipLocked},
		{"SELECT * FROM queue ORDER BY id FOR UPDATE NOWAIT LIMIT 1", LockWaitNoWait},
		{"SELECT * FROM queue ORDER BY id FOR UPDATE LIMIT 1 OFFSET 2", LockWaitBlock},
	}
	for _, tc := range cases {
		t.Run(tc.sql, func(t *testing.T) {
			stmts, err := Parse(tc.sql)
			if err != nil {
				t.Fatalf("Parse(%q) error: %v", tc.sql, err)
			}
			s := stmts[0].(*SelectStmt)
			if len(s.Locking) != 1 {
				t.Fatalf("Locking len = %d, want 1", len(s.Locking))
			}
			if s.Locking[0].WaitPolicy != tc.wantPolicy {
				t.Errorf("WaitPolicy = %v, want %v", s.Locking[0].WaitPolicy, tc.wantPolicy)
			}
			if s.Limit == nil {
				t.Error("Limit nil — trailing LIMIT after locking clause was dropped")
			}
		})
	}
}

// TestParseSelectForRejectsBadStrength — only UPDATE / SHARE are
// accepted in v0; anything else (e.g. FOR READ ONLY, FOR ALL) is
// rejected at parse time.
func TestParseSelectForRejectsBadStrength(t *testing.T) {
	if _, err := Parse("SELECT * FROM t FOR READ"); err == nil {
		t.Error("expected parse error on FOR READ")
	}
}

// TestParseSelectForUpdateRequiresLocked — `SKIP` without
// `LOCKED` is a parse error (mirrors upstream's grammar).
func TestParseSelectForUpdateRequiresLocked(t *testing.T) {
	if _, err := Parse("SELECT * FROM t FOR UPDATE SKIP"); err == nil {
		t.Error("expected parse error on bare SKIP")
	}
}

// TestParseSelectWithoutLockingUnchanged — rollout guardrail:
// pre-M0021 SELECTs produce empty Locking slice, mirroring the
// step-1 invariant established in M0016-0001 / M0017-0001 /
// M0018-0001.
func TestParseSelectWithoutLockingUnchanged(t *testing.T) {
	stmts, err := Parse("SELECT 1")
	if err != nil {
		t.Fatal(err)
	}
	if s := stmts[0].(*SelectStmt); len(s.Locking) != 0 {
		t.Errorf("Locking = %+v, want empty", s.Locking)
	}
}
