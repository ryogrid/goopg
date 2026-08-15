package estimateaudit

import (
	"strings"
	"testing"
)

// pgQ7Bushy is TPC-H Q7 as PG 18.3 plans it on the SF=1 reference cluster
// (analysis/leftdeep-joins/2026-08-05-p56giii-parity.pg.plans.txt), trimmed to
// the join spine with the cost/actual numbers reduced to placeholders the
// parser still accepts. Its top join is 09 §3.10's quoted bushy partition:
// `{customer+lineitem+n2+orders} ⋈ {n1+supplier}`, with the inner side reached
// through a `Hash` wrapper — which is the reason the bushy test has to unwrap
// before it asks "is this child a join?".
const pgQ7Bushy = `GroupAggregate  (cost=1.0..2.0 rows=1 width=0) (actual time=1.0..1.1 rows=4.00 loops=1)
  ->  Sort  (cost=1.0..2.0 rows=1 width=0) (actual time=1.0..1.1 rows=4.00 loops=1)
        ->  Hash Join  (cost=1.0..2.0 rows=12163 width=0) (actual time=1.0..1.1 rows=5869.00 loops=1)
              ->  Nested Loop  (cost=1.0..2.0 rows=1 width=0) (actual time=1.0..1.1 rows=120280.00 loops=1)
                    ->  Nested Loop  (cost=1.0..2.0 rows=1 width=0) (actual time=1.0..1.1 rows=12134.00 loops=1)
                          ->  Hash Join  (cost=1.0..2.0 rows=1 width=0) (actual time=1.0..1.1 rows=12134.00 loops=1)
                                ->  Seq Scan on customer  (cost=1.0..2.0 rows=150000 width=0) (actual time=1.0..1.1 rows=150000.00 loops=1)
                                ->  Hash  (cost=1.0..2.0 rows=2 width=0) (actual time=1.0..1.1 rows=2.00 loops=1)
                                      ->  Seq Scan on nation n2  (cost=1.0..2.0 rows=2 width=0) (actual time=1.0..1.1 rows=2.00 loops=1)
                          ->  Index Scan using order_customer_fkidx on orders  (cost=1.0..2.0 rows=1 width=0) (actual time=1.0..1.1 rows=12134.00 loops=1)
                    ->  Index Scan using idx_lineitem_orderkey_fkidx on lineitem  (cost=1.0..2.0 rows=1 width=0) (actual time=1.0..1.1 rows=120280.00 loops=1)
              ->  Hash  (cost=1.0..2.0 rows=813 width=0) (actual time=1.0..1.1 rows=813.00 loops=1)
                    ->  Hash Join  (cost=1.0..2.0 rows=813 width=0) (actual time=1.0..1.1 rows=813.00 loops=1)
                          ->  Seq Scan on supplier  (cost=1.0..2.0 rows=10000 width=0) (actual time=1.0..1.1 rows=10000.00 loops=1)
                          ->  Hash  (cost=1.0..2.0 rows=2 width=0) (actual time=1.0..1.1 rows=2.00 loops=1)
                                ->  Seq Scan on nation n1  (cost=1.0..2.0 rows=2 width=0) (actual time=1.0..1.1 rows=2.00 loops=1)`

// goopgQ7 is the SAME query from the run-4 flag-ON arm
// (analysis/leftdeep-joins/2026-08-05-p59run4-audit-on.plans.txt), trimmed the
// same way. Its TOP pairing is left-deep — `{…} ⋈ {customer}` — which is the
// divergence 09 §3.10 quotes; note that a join BELOW it is bushy, which is the
// distinction between "the query's chosen spine contains a bushy join" and
// "the query's top partition is the one PG chose".
const goopgQ7 = `Sort  (cost=0.00..0.00 rows=12163 width=0) (actual time=1.0..1.1 rows=4.00 loops=1)
  ->  HashAggregate  (cost=0.00..0.00 rows=12163 width=0) (actual time=1.0..1.1 rows=4.00 loops=1)
    ->  Hash Join  (cost=0.00..0.00 rows=12163 width=0) (actual time=1.0..1.1 rows=5869.00 loops=1)
      ->  Hash Join  (cost=0.00..0.00 rows=304087 width=0) (actual time=1.0..1.1 rows=147791.00 loops=1)
        ->  Hash Join  (cost=0.00..0.00 rows=2534062 width=0) (actual time=1.0..1.1 rows=1820702.00 loops=1)
          ->  Seq Scan on public.lineitem (stats)  (cost=0.00..0.00 rows=5997241 width=0) (actual time=1.0..1.1 rows=5997241.00 loops=1)
          ->  Seq Scan on public.orders (stats)  (cost=0.00..0.00 rows=1500000 width=0) (actual time=1.0..1.1 rows=1500000.00 loops=1)
        ->  Hash Join  (cost=0.00..0.00 rows=1200 width=0) (actual time=1.0..1.1 rows=813.00 loops=1)
          ->  Seq Scan on public.supplier (stats)  (cost=0.00..0.00 rows=10000 width=0) (actual time=1.0..1.1 rows=10000.00 loops=1)
          ->  Nested Loop  (cost=0.00..0.00 rows=3 width=0) (actual time=1.0..1.1 rows=2.00 loops=1)
            ->  Seq Scan on public.nation n1 (stats)  (cost=0.00..0.00 rows=25 width=0) (actual time=1.0..1.1 rows=25.00 loops=1)
            ->  Seq Scan on public.nation n2 (stats)  (cost=0.00..0.00 rows=25 width=0) (actual time=1.0..1.1 rows=25.00 loops=1)
      ->  Seq Scan on public.customer (stats)  (cost=0.00..0.00 rows=150000 width=0) (actual time=1.0..1.1 rows=150000.00 loops=1)`

func spineOf(t *testing.T, name, text string) []SpineJoin {
	t.Helper()
	s := Spine(Audit(name, text))
	if len(s) == 0 {
		t.Fatalf("%s: no join pairings extracted", name)
	}
	return s
}

func findPairing(s []SpineJoin, partition string) *SpineJoin {
	for i := range s {
		if s[i].Partition() == partition {
			return &s[i]
		}
	}
	return nil
}

// The pairing, not the relset, is the unit clause 6 is stated on: both engines
// build Q7's six-relation top joinrel, so the parity channel reports it MATCHED
// while the two engines partition it completely differently.
func TestSpineTopPartitionsDivergeWhereParityReportsAMatch(t *testing.T) {
	g := spineOf(t, "Q7", goopgQ7)
	p := spineOf(t, "Q7", pgQ7Bushy)

	var gTop, pTop *SpineJoin
	for i := range g {
		if g[i].Final {
			gTop = &g[i]
		}
	}
	for i := range p {
		if p[i].Final {
			pTop = &p[i]
		}
	}
	if gTop == nil || pTop == nil {
		t.Fatalf("no final join found: goopg=%v pg=%v", gTop, pTop)
	}
	if got, want := gTop.Partition(), "{lineitem+n1+n2+orders+supplier} ⋈ {customer}"; got != want {
		t.Errorf("goopg top partition = %q, want %q", got, want)
	}
	if got, want := pTop.Partition(), "{customer+lineitem+n2+orders} ⋈ {n1+supplier}"; got != want {
		t.Errorf("PG top partition = %q, want %q", got, want)
	}
	// Same relset on both sides — which is exactly why the parity channel
	// cannot see the divergence.
	if gTop.RelKeyOf() != pTop.RelKeyOf() {
		t.Fatalf("fixture broken: top joinrels differ (%q vs %q)", gTop.RelKeyOf(), pTop.RelKeyOf())
	}
	if gTop.PairKey() == pTop.PairKey() {
		t.Errorf("top pairings compare equal (%q); the diff would be blind to Q7", gTop.PairKey())
	}
}

// The bushy test is about the KIND of both children after the pipeline nodes
// between them are unwrapped — PG reaches Q7's inner join through a `Hash`, and
// a test that stopped at the immediate child would call the bushiest plan in the
// reference set left-deep.
func TestSpineBushyUnwrapsPipelineNodes(t *testing.T) {
	p := spineOf(t, "Q7", pgQ7Bushy)
	top := findPairing(p, "{customer+lineitem+n2+orders} ⋈ {n1+supplier}")
	if top == nil {
		t.Fatalf("PG Q7 top pairing not extracted: %v", p)
	}
	if !top.Bushy {
		t.Errorf("PG Q7 top pairing classed %s; 09 §3.10 measured it BUSHY", top.Shape())
	}
	// `{supplier} ⋈ {n1}` reaches n1 through a Hash too, but a Hash over a
	// SCAN is still a scan — the unwrap must not turn every hash join bushy.
	if inner := findPairing(p, "{supplier} ⋈ {n1}"); inner == nil {
		t.Errorf("PG Q7 {supplier} ⋈ {n1} not extracted: %v", p)
	} else if inner.Bushy {
		t.Errorf("{supplier} ⋈ {n1} classed BUSHY; a Hash over a Seq Scan is not a join")
	}
}

// goopg's run-4 ON-arm Q7 contains a bushy join of its own, one level under a
// left-deep top. The two facts are independent and the instrument must report
// them separately: "goopg never goes bushy" and "goopg did not choose PG's
// partition" are different claims, and run 4 could only score the second.
func TestSpineGoopgQ7IsBushyBelowALeftDeepTop(t *testing.T) {
	g := spineOf(t, "Q7", goopgQ7)
	top := findPairing(g, "{lineitem+n1+n2+orders+supplier} ⋈ {customer}")
	if top == nil {
		t.Fatalf("goopg Q7 top pairing not extracted: %v", g)
	}
	if top.Bushy {
		t.Errorf("goopg Q7 top classed BUSHY; its inner side is a Seq Scan")
	}
	mid := findPairing(g, "{lineitem+orders} ⋈ {n1+n2+supplier}")
	if mid == nil {
		t.Fatalf("goopg Q7 {lineitem+orders} ⋈ {n1+n2+supplier} not extracted: %v", g)
	}
	if !mid.Bushy {
		t.Errorf("goopg Q7 %s classed %s; both children are joins", mid.Partition(), mid.Shape())
	}
}

// The end-to-end shape of the answer clause 6 needs: PG's bushy partition is
// named as a candidate, and goopg's own bushy join does not suppress it.
func TestSpineDiffNamesPGBushyPartitionsGoopgDidNotChoose(t *testing.T) {
	g := []QueryReport{Audit("Q7", goopgQ7)}
	p := []QueryReport{Audit("Q7", pgQ7Bushy)}
	rows := SpineDiff(g, p)
	c := CountSpine(g, p, rows)

	if c.Queries != 1 {
		t.Fatalf("queries compared = %d, want 1", c.Queries)
	}
	if len(c.Candidates) != 1 {
		t.Fatalf("clause-6 candidates = %d, want 1: %v", len(c.Candidates), c.Candidates)
	}
	if got, want := c.Candidates[0].Ref.Partition(), "{customer+lineitem+n2+orders} ⋈ {n1+supplier}"; got != want {
		t.Errorf("candidate = %q, want %q", got, want)
	}
	if len(c.BushyGoopg) != 1 || len(c.BushyRef) != 1 {
		t.Errorf("bushy counts goopg=%v PG=%v, want one query each", c.BushyGoopg, c.BushyRef)
	}
	out := RenderSpine(g, p, rows)
	for _, want := range []string{
		"CLAUSE-6-CANDIDATE",
		"RATCHET spine_pg_only=5 spine_goopg_only=5 bushy_pg=1 bushy_goopg=1 clause6_candidates=1",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("rendered spine section missing %q:\n%s", want, out)
		}
	}
}

// A partition computed from a plan that scans one relation name twice cannot be
// named unambiguously, so it must not become a clause-6 candidate: Q18's
// `lineitem` appears in both the outer join and the aggregated subquery, and
// goopg's EXPLAIN prints both as `lineitem` (09 §4.1's "upper bound" note).
func TestSpineAmbiguousPairingsAreNotCandidates(t *testing.T) {
	g := []QueryReport{Audit("Q18", goopgQ18Shaped)}
	p := []QueryReport{Audit("Q18", pgQ18Shaped)}
	joins := spineOf(t, "Q18", goopgQ18Shaped)
	var sawAmbiguous bool
	for _, j := range joins {
		if j.Ambiguous {
			sawAmbiguous = true
		}
	}
	if !sawAmbiguous {
		t.Fatalf("Q18 fixture no longer trips the duplicate-relation-name gap: %v", joins)
	}
	c := CountSpine(g, p, SpineDiff(g, p))
	for _, r := range c.Candidates {
		if r.Ref.Ambiguous {
			t.Errorf("ambiguous pairing admitted as a clause-6 candidate: %s", r.Ref)
		}
	}
	if len(c.AmbiguousQs) != 1 || c.AmbiguousQs[0] != "Q18" {
		t.Errorf("ambiguous queries = %v, want [Q18]", c.AmbiguousQs)
	}
}

// A query unmeasured on either side is skipped rather than counted clean —
// §5's rule, which the spine channel inherits because a silently absent query
// reads as an agreeing one.
func TestSpineDiffSkipsUnmeasuredQueries(t *testing.T) {
	g := []QueryReport{AuditError("Q7", "timeout"), Audit("Q18", goopgQ18Shaped)}
	p := []QueryReport{Audit("Q7", pgQ7Bushy), AuditError("Q18", "timeout")}
	rows := SpineDiff(g, p)
	if len(rows) != 0 {
		t.Errorf("SpineDiff compared unmeasured queries: %v", rows)
	}
	if c := CountSpine(g, p, rows); c.Queries != 0 {
		t.Errorf("queries compared = %d, want 0", c.Queries)
	}
}
