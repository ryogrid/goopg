package estimateaudit

import (
	"math"
	"strconv"
	"strings"
	"testing"
)

// goopgQ18Shaped is TPC-H Q18 as goopg planned it on 2026-08-05 (SF=1,
// analysis/leftdeep-joins/2026-08-05-p56g.plans.txt), trimmed to the join
// spine: the SEMI lands on the 0.5 punt, est 2 998 620 against an actual 70.
const goopgQ18Shaped = `Sort  (cost=0.00..0.00 rows=100 width=0) (actual time=1.0..1.1 rows=70.00 loops=1)
  ->  Hash Semi Join  (cost=0.00..0.00 rows=2998620 width=0) (actual time=0.9..1.0 rows=70.00 loops=1)
        Hash Cond: (orders.o_orderkey = l_orderkey)
    ->  Hash Join  (cost=0.00..0.00 rows=5997241 width=0) (actual time=0.5..0.8 rows=6001215.00 loops=1)
      ->  Seq Scan on public.customer (stats)  (cost=0.00..0.00 rows=150000 width=0) (actual time=0.1..0.2 rows=150000.00 loops=1)
      ->  Hash Join  (cost=0.00..0.00 rows=5997241 width=0) (actual time=0.3..0.4 rows=6001215.00 loops=1)
        ->  Seq Scan on public.orders (stats)  (cost=0.00..0.00 rows=1500000 width=0) (actual time=0.1..0.2 rows=1500000.00 loops=1)
        ->  Seq Scan on public.lineitem (stats)  (cost=0.00..0.00 rows=5997241 width=0) (actual time=0.1..0.2 rows=5997241.00 loops=1)
    ->  HashAggregate  (cost=0.00..0.00 rows=1210559 width=0) (actual time=0.1..0.2 rows=57.00 loops=1)
      ->  Seq Scan on public.lineitem (stats)  (cost=0.00..0.00 rows=5997241 width=0) (actual time=0.1..0.2 rows=5997241.00 loops=1)`

// pgQ18Shaped is the SAME query as PG 18.3 plans it, in upstream's rendering:
// six-space nesting, the join type spelled in the label, no schema
// qualification, and — the divergence P5.6-g measured — no semi-join at all,
// because `GROUP BY l_orderkey` makes the subquery unique on the join key so
// upstream dedups to a plain inner join. Its final joinrel is 1 674x over.
const pgQ18Shaped = `Sort  (cost=100.0..100.1 rows=100 width=0) (actual time=1.0..1.1 rows=70.00 loops=1)
  ->  Hash Join  (cost=10.0..90.0 rows=117159 width=0) (actual time=0.9..1.0 rows=70.00 loops=1)
        Hash Cond: (orders.o_orderkey = lineitem.l_orderkey)
        ->  Hash Join  (cost=5.0..40.0 rows=5997241 width=0) (actual time=0.5..0.8 rows=6001215.00 loops=1)
              ->  Seq Scan on customer  (cost=0.0..1.0 rows=150000 width=0) (actual time=0.1..0.2 rows=150000.00 loops=1)
              ->  Hash Join  (cost=1.0..20.0 rows=5997241 width=0) (actual time=0.3..0.4 rows=6001215.00 loops=1)
                    ->  Seq Scan on orders  (cost=0.0..1.0 rows=1500000 width=0) (actual time=0.1..0.2 rows=1500000.00 loops=1)
                    ->  Seq Scan on lineitem  (cost=0.0..1.0 rows=5997241 width=0) (actual time=0.1..0.2 rows=5997241.00 loops=1)
        ->  HashAggregate  (cost=1.0..2.0 rows=57 width=0) (actual time=0.1..0.2 rows=57.00 loops=1)
              ->  Seq Scan on lineitem  (cost=0.0..1.0 rows=5997241 width=0) (actual time=0.1..0.2 rows=5997241.00 loops=1)`

func TestJoinLabelsCoverUpstreamSpelling(t *testing.T) {
	// §4 audits a PG plan through this parser. Upstream spells the join type
	// in the label; a reference plan whose joins failed to classify would
	// read as a query PG estimated perfectly.
	cases := map[string]bool{
		"Hash Anti Join":         true,
		"Hash Semi Join":         true,
		"Hash Right Anti Join":   true,
		"Merge Left Join":        true,
		"Nested Loop Anti Join":  true,
		"Nested Loop Left Join":  true,
		"Hash Join (ANTI)":       true, // legacy parenthetical spelling, still classified
		"Hash":                   false,
		"HashAggregate":          false,
		"Merge Append":           false,
		"Bitmap Heap Scan on t":  false,
		"Subquery Scan on x":     false,
		"Gather":                 false,
		"Incremental Sort":       false,
		"Seq Scan on join_table": false,
	}
	for label, want := range cases {
		if got := isJoinLabel(label); got != want {
			t.Errorf("isJoinLabel(%q) = %v, want %v", label, got, want)
		}
	}
}

func TestLeafRelNormalisesBothRenderings(t *testing.T) {
	cases := []struct {
		label string
		want  string
		ok    bool
	}{
		{"Seq Scan on public.lineitem (stats)", "lineitem", true},
		{"Seq Scan on lineitem", "lineitem", true},
		{"Seq Scan on public.lineitem l1 (stats)", "l1", true}, // alias wins
		{"Seq Scan on lineitem l1", "l1", true},                // ... on both sides
		{"Parallel Seq Scan on lineitem", "lineitem", true},    // upstream
		{"Index Scan using public.orders_pk on public.orders", "orders", true},
		{"Index Scan using orders_pkey on orders o", "o", true},
		{"Index Only Scan using public.part_pk on public.part", "part", true},
		{"Bitmap Heap Scan on lineitem", "lineitem", true},
		{"Bitmap Index Scan on lineitem_l_orderkey_idx", "", false}, // an INDEX, not a rel
		{"Hash Join (INNER)", "", false},
		{"HashAggregate", "", false},
		{"Hash", "", false},
	}
	for _, c := range cases {
		got, ok := leafRel(c.label)
		if ok != c.ok || got != c.want {
			t.Errorf("leafRel(%q) = (%q,%v), want (%q,%v)", c.label, got, ok, c.want, c.ok)
		}
	}
}

func TestJoinrelIdentityIsIndentationStepIndependent(t *testing.T) {
	// goopg indents two spaces per level, upstream six. Any arithmetic on
	// depth would derive a different tree from the two renderings of the
	// same shape; the rel sets must come out identical anyway.
	g := Audit("Q18", goopgQ18Shaped)
	p := Audit("Q18", pgQ18Shaped)
	if g.Final == nil || p.Final == nil {
		t.Fatalf("final joinrel missing: goopg=%v pg=%v", g.Final, p.Final)
	}
	want := "customer+lineitem+orders"
	if got := g.Final.RelKey(); got != want {
		t.Errorf("goopg final joinrel = %q, want %q", got, want)
	}
	if got := p.Final.RelKey(); got != want {
		t.Errorf("PG final joinrel = %q, want %q", got, want)
	}
	// The deepest two-way is the same joinrel on both sides too.
	if got, want := g.Joins[2].RelKey(), "lineitem+orders"; got != want {
		t.Errorf("goopg inner joinrel = %q, want %q", got, want)
	}
	// ... and Q18 is also the live case for identity AMBIGUITY: the
	// subquery's lineitem is a different range-table entry from the outer
	// query's, but neither engine prints an alias for it, so the SEMI keys
	// the same as the inner join below it. The printed plan cannot separate
	// them; the gate must say so rather than pick one silently.
	if got, want := g.Joins[1].RelKey(), g.Final.RelKey(); got != want {
		t.Fatalf("fixture no longer reproduces the Q18 key collision: %q vs %q", got, want)
	}
}

func TestParityMatchesJoinrelsAcrossDifferentJoinOrders(t *testing.T) {
	// The premise of the gate: a joinrel is its SET of base relations, so
	// {a,b,c} on one side matches {a,b,c} on the other however each engine
	// got there. Here goopg builds (a⋈b)⋈c and the reference builds
	// (a⋈c)⋈b — only the final joinrel is shared, and it must match.
	goopgPlan := `Hash Join  (cost=0.00..0.00 rows=100 width=0) (actual rows=1000.00 loops=1)
  ->  Hash Join  (cost=0.00..0.00 rows=50 width=0) (actual rows=500.00 loops=1)
    ->  Seq Scan on public.a  (cost=0.00..0.00 rows=10 width=0) (actual rows=10.00 loops=1)
    ->  Seq Scan on public.b  (cost=0.00..0.00 rows=10 width=0) (actual rows=10.00 loops=1)
  ->  Seq Scan on public.c  (cost=0.00..0.00 rows=10 width=0) (actual rows=10.00 loops=1)`
	refPlan := `Hash Join  (cost=1.0..2.0 rows=100 width=0) (actual rows=1000.00 loops=1)
  ->  Hash Join  (cost=1.0..2.0 rows=60 width=0) (actual rows=600.00 loops=1)
        ->  Seq Scan on a  (cost=0.0..1.0 rows=10 width=0) (actual rows=10.00 loops=1)
        ->  Seq Scan on c  (cost=0.0..1.0 rows=10 width=0) (actual rows=10.00 loops=1)
  ->  Seq Scan on b  (cost=0.0..1.0 rows=10 width=0) (actual rows=10.00 loops=1)`

	rows := Parity([]QueryReport{Audit("Q1", goopgPlan)}, []QueryReport{Audit("Q1", refPlan)})
	var matched, shape int
	for _, r := range rows {
		if r.Status == ParityMatched {
			matched++
			if strings.Join(r.Rels, "+") != "a+b+c" {
				t.Errorf("matched row = %q, want the final joinrel a+b+c", strings.Join(r.Rels, "+"))
			}
			continue
		}
		shape++
	}
	if matched != 1 || shape != 2 {
		t.Fatalf("got %d matched / %d shape-divergent rows, want 1/2:\n%v", matched, shape, rows)
	}
	// A joinrel only one engine built is a SHAPE divergence, not a parity
	// failure — there is nothing to compare it against.
	if v := ParityViolations(rows, DefaultParityBar()); len(v) != 0 {
		t.Errorf("shape divergences must not count as parity violations: %v", v)
	}
	if m := ParityMismatches(rows); len(m) != 2 {
		t.Errorf("ParityMismatches = %d rows, want 2", len(m))
	}
}

func TestParityFlagsQ18AndNotPGsOwnMisestimate(t *testing.T) {
	// The finding that motivated the gate: at 1 674x PG ITSELF is over §5's
	// 10³ tripwire on Q18, so the absolute factor cannot be the bar. Parity
	// still flags goopg's 42 837x, because it is 25.6x worse than the
	// reference.
	g := Audit("Q18", goopgQ18Shaped)
	p := Audit("Q18", pgQ18Shaped)

	if got := p.Final.Ratio(); got < 1600 || got > 1700 {
		t.Fatalf("reference final-joinrel ratio = %.0fx, want ~1674x (the measured PG 18.3 value)", got)
	}
	if v := Violations([]QueryReport{p}, DefaultThresholds()); len(v) == 0 {
		t.Fatal("the fixture must reproduce the finding: PG's own Q18 estimate trips the absolute tripwire")
	}

	rows := Parity([]QueryReport{g}, []QueryReport{p})
	v := ParityViolations(rows, DefaultParityBar())
	if len(v) != 1 {
		t.Fatalf("got %d parity violations, want 1 (the final joinrel):\n%s",
			len(v), RenderParity([]QueryReport{g}, []QueryReport{p}, rows, DefaultParityBar()))
	}
	if !v[0].Final || strings.Join(v[0].Rels, "+") != "customer+lineitem+orders" {
		t.Errorf("violation = %s, want the final joinrel", v[0])
	}
	if got := v[0].Excess(); got < 20 || got > 30 {
		t.Errorf("excess = %.1fx, want ~25.6x (42 837 / 1 674)", got)
	}
	if !v[0].Ambiguous {
		t.Error("Q18's final joinrel keys the same as the join below it (unaliased second lineitem); the row must say so")
	}
	if out := RenderParity([]QueryReport{g}, []QueryReport{p}, rows, DefaultParityBar()); !strings.Contains(out, "ambiguous") {
		t.Errorf("the ambiguity must reach the committed artifact:\n%s", out)
	}
	// The inner {lineitem,orders} joinrel is estimated identically by both,
	// so it must not appear.
	for _, r := range rows {
		if strings.Join(r.Rels, "+") == "lineitem+orders" && r.Violates(DefaultParityBar()) {
			t.Errorf("a joinrel both engines estimate alike must not violate: %s", r)
		}
	}
}

func TestParityFloorKeepsAccurateJoinrelsOutOfTheRatchet(t *testing.T) {
	// PG exact, goopg 20x off: 20x the reference's factor, but 20x absolute
	// is not what §4's ratchet is for. Without the floor every joinrel PG
	// happens to nail would enter the ratchet.
	goopgPlan := `Hash Join  (cost=0.00..0.00 rows=50 width=0) (actual rows=1000.00 loops=1)
  ->  Seq Scan on public.a  (cost=0.00..0.00 rows=10 width=0) (actual rows=10.00 loops=1)
  ->  Seq Scan on public.b  (cost=0.00..0.00 rows=10 width=0) (actual rows=10.00 loops=1)`
	refPlan := `Hash Join  (cost=1.0..2.0 rows=1000 width=0) (actual rows=1000.00 loops=1)
  ->  Seq Scan on a  (cost=0.0..1.0 rows=10 width=0) (actual rows=10.00 loops=1)
  ->  Seq Scan on b  (cost=0.0..1.0 rows=10 width=0) (actual rows=10.00 loops=1)`
	rows := Parity([]QueryReport{Audit("Q1", goopgPlan)}, []QueryReport{Audit("Q1", refPlan)})
	if len(rows) != 1 || rows[0].Status != ParityMatched {
		t.Fatalf("expected one matched row, got %v", rows)
	}
	if got := rows[0].Excess(); math.Abs(got-20) > 0.1 {
		t.Fatalf("excess = %v, want 20", got)
	}
	if rows[0].Violates(DefaultParityBar()) {
		t.Error("a 20x-off joinrel is under the 100x floor and must not enter the ratchet")
	}
	// Drop the floor and the same row does count — the floor is the reason,
	// not the slack.
	if !rows[0].Violates(ParityBar{Slack: DefaultParitySlack, Floor: 1}) {
		t.Error("with the floor removed the 20x excess must count")
	}
}

func TestParityCreditsGoopgWhenItBeatsTheReference(t *testing.T) {
	// goopg 2x off where PG is 1 000x off: excess 0.002. A gate that only
	// looked at absolute factors would say nothing here; the parity column
	// has to be able to say "better than PG".
	goopgPlan := `Hash Join  (cost=0.00..0.00 rows=500 width=0) (actual rows=1000.00 loops=1)
  ->  Seq Scan on public.a  (cost=0.00..0.00 rows=10 width=0) (actual rows=10.00 loops=1)
  ->  Seq Scan on public.b  (cost=0.00..0.00 rows=10 width=0) (actual rows=10.00 loops=1)`
	refPlan := `Hash Join  (cost=1.0..2.0 rows=1 width=0) (actual rows=1000.00 loops=1)
  ->  Seq Scan on a  (cost=0.0..1.0 rows=10 width=0) (actual rows=10.00 loops=1)
  ->  Seq Scan on b  (cost=0.0..1.0 rows=10 width=0) (actual rows=10.00 loops=1)`
	rows := Parity([]QueryReport{Audit("Q1", goopgPlan)}, []QueryReport{Audit("Q1", refPlan)})
	if got := rows[0].Excess(); got > 0.01 {
		t.Errorf("excess = %v, want <0.01 (goopg 2x vs PG 1000x)", got)
	}
	if rows[0].Violates(DefaultParityBar()) {
		t.Error("beating the reference must never be a violation")
	}
}

func TestQ21FinalBarIsAParityBarNotAMute(t *testing.T) {
	// Q21's ANTI: est 1 against an actual 4 003 — and PG 18.3 estimates
	// rows=1 too (neqjoinsel returns 1-nullfrac for JOIN_ANTI by design), so
	// holding it to the absolute tripwire would demand a divergence from PG.
	q21 := func(est int) string {
		return `HashAggregate  (cost=0.00..0.00 rows=10000 width=0) (actual rows=405.00 loops=1)
  ->  Hash Anti Join  (cost=0.00..0.00 rows=` + strconv.Itoa(est) + ` width=0) (actual rows=4003.00 loops=1)
    ->  Seq Scan on public.lineitem l1 (stats)  (cost=0.00..0.00 rows=5997241 width=0) (actual rows=5997241.00 loops=1)
    ->  Seq Scan on public.lineitem l3 (stats)  (cost=0.00..0.00 rows=5997241 width=0) (actual rows=5997241.00 loops=1)`
	}
	if v := Violations([]QueryReport{Audit("Q21", q21(1))}, DefaultThresholds()); len(v) != 0 {
		t.Errorf("Q21's measured-PG-parity ANTI must pass its per-query bar: %v", v)
	}
	// It is a BAR, not an exemption: a genuine regression of the same
	// joinrel still trips it. est=1 actual=40 000 000 is 4e7x.
	regressed := strings.Replace(q21(1), "actual rows=4003.00", "actual rows=40000000.00", 1)
	if v := Violations([]QueryReport{Audit("Q21", regressed)}, DefaultThresholds()); len(v) != 1 {
		t.Errorf("a regressed Q21 ANTI must still violate; got %v", v)
	}
	// And the reason travels with the number into the committed artifact.
	out := Render([]QueryReport{Audit("Q21", q21(1))}, DefaultThresholds())
	if !strings.Contains(out, "OVERRIDE Q21") || !strings.Contains(out, "neqjoinsel") {
		t.Errorf("the per-query bar must render its justification:\n%s", out)
	}
}

func TestQ21SelfJoinAliasesKeepJoinrelsDistinct(t *testing.T) {
	// Q21 scans lineitem three times. Without the alias every leaf keys to
	// "lineitem" and the SEMI, the ANTI and the base scan collapse into one
	// joinrel identity.
	plan := `Hash Anti Join  (cost=0.00..0.00 rows=1 width=0) (actual rows=4003.00 loops=1)
  ->  Hash Semi Join  (cost=0.00..0.00 rows=248 width=0) (actual rows=71824.00 loops=1)
    ->  Seq Scan on public.lineitem l1 (stats)  (cost=0.00..0.00 rows=5997241 width=0) (actual rows=5997241.00 loops=1)
    ->  Seq Scan on public.lineitem l2 (stats)  (cost=0.00..0.00 rows=5997241 width=0) (actual rows=5997241.00 loops=1)
  ->  Seq Scan on public.lineitem l3 (stats)  (cost=0.00..0.00 rows=5997241 width=0) (actual rows=5997241.00 loops=1)`
	r := Audit("Q21", plan)
	if len(r.Joins) != 2 {
		t.Fatalf("got %d joins, want 2", len(r.Joins))
	}
	if got, want := r.Joins[0].RelKey(), "l1+l2+l3"; got != want {
		t.Errorf("ANTI joinrel = %q, want %q", got, want)
	}
	if got, want := r.Joins[1].RelKey(), "l1+l2"; got != want {
		t.Errorf("SEMI joinrel = %q, want %q", got, want)
	}
}

func TestParitySkipsAndNamesUncomparableQueries(t *testing.T) {
	g := []QueryReport{
		Audit("Q1", goopgQ18Shaped),
		AuditError("Q21", "timeout after 150s"),
		Audit("Q22", goopgQ18Shaped),
	}
	ref := []QueryReport{
		Audit("Q1", pgQ18Shaped),
		Audit("Q21", pgQ18Shaped),
	}
	rows := Parity(g, ref)
	for _, r := range rows {
		if r.Query != "Q1" {
			t.Errorf("row for an uncomparable query leaked in: %s", r)
		}
	}
	out := RenderParity(g, ref, rows, DefaultParityBar())
	// §5's rule, applied to the parity column: a query silently absent from
	// the comparison reads as a compared-and-clean one.
	for _, want := range []string{"UNCOMPARED Q21", "UNCOMPARED Q22", "goopg unmeasured", "no reference plan"} {
		if !strings.Contains(out, want) {
			t.Errorf("parity render missing %q:\n%s", want, out)
		}
	}
}

func TestRenderParityIsDeterministicAndReportsTheRatchet(t *testing.T) {
	g := []QueryReport{Audit("Q18", goopgQ18Shaped)}
	p := []QueryReport{Audit("Q18", pgQ18Shaped)}
	rows := Parity(g, p)
	out := RenderParity(g, p, rows, DefaultParityBar())
	if out != RenderParity(g, p, rows, DefaultParityBar()) {
		t.Fatal("RenderParity is not deterministic — the committed artifact would diff on every run")
	}
	for _, want := range []string{"PER-JOINREL PARITY", "excess", "RATCHET parity_violations=1", "shape_mismatches="} {
		if !strings.Contains(out, want) {
			t.Errorf("parity render missing %q:\n%s", want, out)
		}
	}
}

func TestSplitPlansFileReplaysACommittedArtifact(t *testing.T) {
	// The committed .plans.txt is `=== <name>` followed by the raw plan, and
	// replaying it must reconstruct exactly the audit the live run produced
	// — that is what lets a NEW instrument be applied to OLD evidence.
	artifact := "=== Q18\n" + goopgQ18Shaped + "\n\n=== Q21\nUNMEASURED\n\n"
	got := SplitPlansFile(artifact)
	if len(got) != 2 {
		t.Fatalf("split into %d reports, want 2", len(got))
	}
	live := Audit("Q18", goopgQ18Shaped)
	if got[0].Name != "Q18" || len(got[0].Joins) != len(live.Joins) || got[0].Final.EstRows != live.Final.EstRows {
		t.Errorf("replayed report != live report: %+v vs %+v", got[0].Final, live.Final)
	}
	if got[1].Err == "" {
		t.Errorf("a plan-less block must replay as unmeasured, not as a clean audit: %+v", got[1])
	}
}
