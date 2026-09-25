package executor

// R50 Slice A pins (design `r50-hash-joinfilter-dedup/DESIGN.md`): admitting
// the bpchar family (`char`, `bpchar`, `character`) into the hash-safe set
// removes the `Hash Cond:` + `Join Filter:` duplication on char-keyed hash
// joins — the conjunct folds into the key encoding and leaves the residual.
//
// Graduated from the throwaway `zz_r50_probe_test.go` matrix
// ({char, varchar} x {base, CTE}), which showed the discriminator is purely
// the type-name whitelist: varchar shapes were already HC-only pre-slice,
// char shapes duplicated, and char-CTE duplication proved CTE output
// schemas propagate types correctly.

import (
	"strings"
	"testing"
)

// bpcharJoinFixture builds the probe matrix as permanent fixture: char(16)
// and varchar(50) base pairs with identical values, NULL-key rows on both
// char sides (NULL keys never meet AND NULL `=` is never true, so they are
// inert in every test below), plus the direction-1 shapes.
func bpcharJoinFixture(t *testing.T) (*Context, func()) {
	t.Helper()
	ctx, _, cleanup := newDDLFixture(t)
	stmts := []string{
		"CREATE TABLE ka (id char(16), v int)",
		"CREATE TABLE kb (id char(16), w int)",
		"CREATE TABLE va (id varchar(50), v int)",
		"CREATE TABLE vb (id varchar(50), w int)",
		"INSERT INTO ka VALUES ('a', 1), ('b', 2), (NULL, 3)",
		"INSERT INTO kb VALUES ('a', 10), ('c', 30), (NULL, 40)",
		"INSERT INTO va VALUES ('a', 1), ('b', 2), (NULL, 3)",
		"INSERT INTO vb VALUES ('a', 10), ('c', 30), (NULL, 40)",
		// Unbounded bpchar stores verbatim (no width to trim to): the
		// trailing-space pair survives storage byte-distinct.
		"CREATE TABLE ta (id bpchar)",
		"CREATE TABLE tb (id bpchar)",
		"INSERT INTO ta VALUES ('ab')",
		"INSERT INTO tb VALUES ('ab ')",
		// UUID case variants: compareDatum normalizes both sides, so `=`
		// is true while the key encodings differ.
		"CREATE TABLE ua (id char(36), v int)",
		"CREATE TABLE ub (id char(36), w int)",
		"INSERT INTO ua VALUES ('A8098C5A-9E1B-4D6A-8F0C-123456789ABC', 1)",
		"INSERT INTO ub VALUES ('a8098c5a-9e1b-4d6a-8f0c-123456789abc', 10)",
		"CREATE TABLE xa (id varchar(50), v int)",
		"CREATE TABLE xb (id varchar(50), w int)",
		"INSERT INTO xa VALUES ('A8098C5A-9E1B-4D6A-8F0C-123456789ABC', 1)",
		"INSERT INTO xb VALUES ('a8098c5a-9e1b-4d6a-8f0c-123456789abc', 10)",
		// Q59-shape doll-houses: two equi-pairs, and equi + non-equi.
		"CREATE TABLE ma (id char(16), n int, v int)",
		"CREATE TABLE mb (id char(16), n int, w int)",
		"INSERT INTO ma VALUES ('a', 1, 100), ('b', 2, 200)",
		"INSERT INTO mb VALUES ('a', 1, 10), ('b', 2, 20)",
		"CREATE TABLE mq (id char(16), n int, v int)",
		"CREATE TABLE mr (id char(16), n int, w int)",
		"INSERT INTO mq VALUES ('a', 5, 100), ('b', 2, 200)",
		"INSERT INTO mr VALUES ('a', 1, 10), ('b', 9, 20)",
	}
	for _, s := range stmts {
		if err := runDDL(t, ctx, s); err != nil {
			t.Fatalf("fixture %q: %v", s, err)
		}
	}
	return ctx, cleanup
}

func bpcharExplain(t *testing.T, ctx *Context, sql string) string {
	t.Helper()
	return strings.Join(runExplainRows(t, ctx, "EXPLAIN (COSTS OFF) "+sql), "\n")
}

func bpcharValues(t *testing.T, ctx *Context, sql string) string {
	t.Helper()
	rows := runQueryRows(t, ctx, sql)
	vals := make([]string, 0, len(rows))
	for _, r := range rows {
		cells := make([]string, 0, len(r))
		for _, d := range r {
			cells = append(cells, d.Format())
		}
		vals = append(vals, strings.Join(cells, "|"))
	}
	return strings.Join(vals, ",")
}

// TestHashJoinBpcharRendersHashCondOnly is the Step-0 pin: the slice's
// visible consequence. char-base and char-CTE joins print HC-only (the dup
// is gone); varchar-base is the unchanged status-quo control. HC text is
// asserted byte-exact — it renders `HashKeys`, not `Keys`, so folding
// pairs into the encoding is text-invariant by construction, and this
// locks that in.
func TestHashJoinBpcharRendersHashCondOnly(t *testing.T) {
	ctx, cleanup := bpcharJoinFixture(t)
	defer cleanup()

	for _, tc := range []struct{ name, sql, wantHC string }{
		{"char-base", "SELECT ka.v FROM ka JOIN kb ON ka.id = kb.id", "(ka.id = kb.id)"},
		{"varchar-base", "SELECT va.v FROM va JOIN vb ON va.id = vb.id", "(va.id = vb.id)"},
		// CTE arms render the right side bare (`(x.id = id)`) — a
		// pre-existing CTE-scan render quirk, not this slice's business.
		// What matters here is HC-only on both CTE shapes.
		{"char-cte", "WITH x AS (SELECT id, v FROM ka), y AS (SELECT id, w FROM kb) SELECT x.v FROM x JOIN y ON x.id = y.id", "(x.id = id)"},
		{"varchar-cte", "WITH x AS (SELECT id, v FROM va), y AS (SELECT id, w FROM vb) SELECT x.v FROM x JOIN y ON x.id = y.id", "(x.id = id)"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			plan := bpcharExplain(t, ctx, tc.sql)
			if !strings.Contains(plan, "Hash Join") {
				t.Skipf("planner did not pick a hash join; got:\n%s", plan)
			}
			if got := condLine(t, plan, "Hash Cond: "); got != tc.wantHC {
				t.Errorf("Hash Cond = %q, want %q\nplan:\n%s", got, tc.wantHC, plan)
			}
			if strings.Contains(plan, "Join Filter") {
				t.Errorf("char-keyed all-equi join still prints a residual it does not evaluate:\n%s", plan)
			}
		})
	}
}

// TestHashJoinBpcharValues pins that folding the conjunct into the key
// encoding changes no rows: the char join and the varchar control return
// the same single matching row, and the NULL-key rows on both sides match
// nothing (NULL keys never meet per `encodeCompositeKey`, NULL `=` is
// never true — no change by this slice either way).
func TestHashJoinBpcharValues(t *testing.T) {
	ctx, cleanup := bpcharJoinFixture(t)
	defer cleanup()

	if got := bpcharValues(t, ctx, "SELECT ka.v FROM ka JOIN kb ON ka.id = kb.id ORDER BY ka.v"); got != "1" {
		t.Errorf("char join returned %q, want \"1\"", got)
	}
	if got := bpcharValues(t, ctx, "SELECT va.v FROM va JOIN vb ON va.id = vb.id ORDER BY va.v"); got != "1" {
		t.Errorf("varchar control join returned %q, want \"1\"", got)
	}
}

// TestHashJoinBpcharTrailingSpaceAgreement documents the verbatim-storage
// corner: unbounded bpchar keeps 'ab ' byte-distinct (octet_length 3 vs
// 2), goopg's `=` is byte-exact on strings so the scalar equality is
// FALSE, and the join is empty. Keys and `=` agree — both miss — so
// folding changes nothing here: residual-false ⟺ no emit pre-slice,
// no-meet ⟺ no emit post-slice. (PG would match under bpchareq's
// trailing-space rule; goopg misses either way — UNCHANGED by this
// slice, values work if ever.)
func TestHashJoinBpcharTrailingSpaceAgreement(t *testing.T) {
	ctx, cleanup := bpcharJoinFixture(t)
	defer cleanup()

	if got := bpcharValues(t, ctx, "SELECT octet_length(id) FROM ta"); got != "2" {
		t.Errorf("verbatim premise broken: ta octet_length = %q, want \"2\"", got)
	}
	if got := bpcharValues(t, ctx, "SELECT octet_length(id) FROM tb"); got != "3" {
		t.Errorf("verbatim premise broken: tb octet_length = %q, want \"3\"", got)
	}
	if got := bpcharValues(t, ctx, "SELECT ta.id = tb.id FROM ta, tb"); got != "f" {
		t.Errorf("scalar `=` = %q, want \"f\" (byte-exact strings); if this flips, the agreement argument below must be re-derived", got)
	}
	if got := bpcharValues(t, ctx, "SELECT COUNT(*) FROM ta JOIN tb ON ta.id = tb.id"); got != "0" {
		t.Errorf("trailing-space join count = %q, want \"0\"", got)
	}
}

// TestHashJoinBpcharUUIDCaseMissParity pins the review-note-1 direction-1
// class: `compareDatum` normalizes UUID case, so the scalar `=` is TRUE,
// but the key encodings (`"s:" + raw bytes`) differ and the rows never
// meet — the join is empty. Pre-slice the char pair met via the residual
// (count 1); post-slice it misses (count 0). The varchar pair is the
// status-quo control: the SAME folding was accepted for varchar at P2.2,
// so varchar misses identically — the slice introduces no NEW behavior
// class, only parity with the existing one. Corpus-harmless (no in-scope
// TPC-DS values have these shapes).
func TestHashJoinBpcharUUIDCaseMissParity(t *testing.T) {
	ctx, cleanup := bpcharJoinFixture(t)
	defer cleanup()

	if got := bpcharValues(t, ctx, "SELECT ua.id = ub.id FROM ua, ub"); got != "t" {
		t.Fatalf("normalizer premise broken: char UUID `=` = %q, want \"t\"", got)
	}
	if got := bpcharValues(t, ctx, "SELECT xa.id = xb.id FROM xa, xb"); got != "t" {
		t.Fatalf("normalizer premise broken: varchar UUID `=` = %q, want \"t\"", got)
	}
	if got := bpcharValues(t, ctx, "SELECT COUNT(*) FROM ua JOIN ub ON ua.id = ub.id"); got != "0" {
		t.Errorf("char UUID-case join count = %q, want \"0\" (direction-1 miss after folding)", got)
	}
	if got := bpcharValues(t, ctx, "SELECT COUNT(*) FROM xa JOIN xb ON xa.id = xb.id"); got != "0" {
		t.Errorf("varchar UUID-case control count = %q, want \"0\" (P2.2 status quo)", got)
	}
}

// TestHashJoinBpcharMultiKey is the Q59-shape doll-house at fixture level:
// a two-equi char+int join folds BOTH pairs (HC shows both, no JF), while
// an equi + non-equi join folds only the char pair and the JF keeps
// exactly the non-equi remainder.
func TestHashJoinBpcharMultiKey(t *testing.T) {
	ctx, cleanup := bpcharJoinFixture(t)
	defer cleanup()

	plan := bpcharExplain(t, ctx, "SELECT ma.v FROM ma JOIN mb ON ma.id = mb.id AND ma.n = mb.n")
	if !strings.Contains(plan, "Hash Join") {
		t.Skipf("planner did not pick a hash join; got:\n%s", plan)
	}
	if got := condLine(t, plan, "Hash Cond: "); got != "((ma.id = mb.id) AND (ma.n = mb.n))" {
		t.Errorf("two-pair Hash Cond = %q, want both pairs\nplan:\n%s", got, plan)
	}
	if strings.Contains(plan, "Join Filter") {
		t.Errorf("two-equi join printed a residual it does not evaluate:\n%s", plan)
	}
	if got := bpcharValues(t, ctx, "SELECT ma.v FROM ma JOIN mb ON ma.id = mb.id AND ma.n = mb.n ORDER BY ma.v"); got != "100,200" {
		t.Errorf("two-equi join returned %q, want \"100,200\"", got)
	}

	plan = bpcharExplain(t, ctx, "SELECT mq.v FROM mq JOIN mr ON mq.id = mr.id AND mq.n > mr.n")
	if !strings.Contains(plan, "Hash Join") {
		t.Skipf("planner did not pick a hash join; got:\n%s", plan)
	}
	if got := condLine(t, plan, "Hash Cond: "); got != "(mq.id = mr.id)" {
		t.Errorf("Hash Cond = %q, want the char pair only\nplan:\n%s", got, plan)
	}
	if got := condLine(t, plan, "Join Filter: "); got != "(mq.n > mr.n)" {
		t.Errorf("Join Filter = %q, want exactly the non-equi remainder\nplan:\n%s", got, plan)
	}
	if got := bpcharValues(t, ctx, "SELECT mq.v FROM mq JOIN mr ON mq.id = mr.id AND mq.n > mr.n ORDER BY mq.v"); got != "100" {
		t.Errorf("mixed join returned %q, want \"100\"", got)
	}
}
