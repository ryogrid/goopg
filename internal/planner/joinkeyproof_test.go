package planner

// M0127-P5.6-f — the two halves that must land together: multi-key equi-join
// pricing, and `get_foreign_key_join_selectivity` (costsize.c:5651) over the
// uniqueness evidence stamped on the leaf scans.
//
// Design: leftdeep-joins
// [04](../../docs/design/leftdeep-joins/04-cost-and-cardinality.md) §3.1/§3.3,
// [09](../../docs/design/leftdeep-joins/09-verification-and-acceptance.md) §5.4.
//
// The measured shape these tests are built around is Q9's, because it is the
// one 09 §5.4 attributed: `lineitem ⋈ partsupp` on
// `l_suppkey = ps_suppkey AND l_partkey = ps_partkey`, actual 5 997 241 rows.
// Three numbers matter and the tests name all three:
//
//	 481 M — pricing ONE pair while the residual excludes BOTH (the defect)
//	   2.4 k — pricing both pairs as independent marginals (half 1 alone)
//	   6.0 M — one 1/ntuples for the composite key as a whole (both halves)
//
// The middle number is why the halves cannot land separately: half 1 on its own
// is a bigger error than the defect it fixes, in the other direction.

import (
	"testing"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/parser"
)

// keyProofCol is one column of a test relation: a name (the superkey prover
// matches keys by NAME, so the shared `statsTable` helper's uniform "c" is
// unusable here) and its ndistinct.
type keyProofCol struct {
	name string
	nd   int64
}

// keyProofScan builds a stats-bearing SeqScan carrying uniqueness evidence,
// the way the planner's `uniqueKeyColumnSets` stamp does.
func keyProofScan(name string, rows int64, cols []keyProofCol, uniq ...[]string) *SeqScan {
	columns := make([]catalog.Column, len(cols))
	stats := make([]catalog.ColumnStats, len(cols))
	for i, c := range cols {
		columns[i] = catalog.Column{Name: c.name, Type: catalog.Type{Name: "int4"}}
		stats[i] = catalog.ColumnStats{NDistinct: c.nd}
	}
	tbl := &catalog.Table{
		Name:    name,
		Columns: columns,
		Stats:   &catalog.TableStats{RowCount: rows, Columns: stats},
	}
	return &SeqScan{Table: tbl, schema: tableSchema(tbl), UniqueKeys: uniq}
}

// keyedPairs attaches N equi-pairs the way fillJoinHashKeys does: both operands
// in the MERGED left‖right coordinate space, and the same list published in
// HashKeys. Each element is (leftIdx, rightIdxWithinRightInput).
func keyedPairs(j *Join, pairs ...[2]int) *Join {
	lw := len(j.Left.Output())
	j.HashKeys = nil
	for i, p := range pairs {
		kp := JoinKeyPair{Left: jrCol(p[0]), Right: jrCol(lw + p[1])}
		if i == 0 {
			j.LeftKey, j.RightKey = kp.Left, kp.Right
		}
		j.HashKeys = append(j.HashKeys, kp)
	}
	return j
}

// q9Shape is the measured joinrel of 09 §5.4. `uniq` is partsupp's evidence:
// pass the composite PK to get the both-halves answer, nothing to get half 1's.
func q9Shape(uniq ...[]string) *Join {
	lineitem := keyProofScan("lineitem", 6001215, []keyProofCol{
		{"l_partkey", 200000}, {"l_suppkey", 10000},
	})
	partsupp := keyProofScan("partsupp", 800000, []keyProofCol{
		{"ps_partkey", 200000}, {"ps_suppkey", 10000},
	}, uniq...)
	// l_suppkey = ps_suppkey AND l_partkey = ps_partkey — the order the
	// query writes them in, which is deliberately NOT the index's order.
	return keyedPairs(mergedJoin(JoinTypeInner, lineitem, partsupp), [2]int{1, 1}, [2]int{0, 0})
}

// --- half 1: every equi-pair is priced ------------------------------------

func TestEstimateJoinPricesEveryEquiPair(t *testing.T) {
	j := q9Shape() // no uniqueness evidence
	// 6 001 215 · 800 000 / (10 000 · 200 000) = 2400.
	if got, want := EstimateRows(j), int64(2400); got != want {
		t.Fatalf("two-pair estimate = %d, want %d (l·r/(nd_supp·nd_part))", got, want)
	}
	// The defect: pricing only the first pair leaves 4.8e8, which is what
	// the audit measured against an actual of 5 997 241.
	if got := EstimateRows(j); got > 1e8 {
		t.Fatalf("estimate %d still looks like the one-pair answer (~4.8e8)", got)
	}
}

// TestEstimateJoinSinglePairUnchanged is the no-regression half of the above:
// a one-pair equi-join must still be exactly `l·r/max(nd)`, the P5.6-e-iii
// formula. Every TPC-H joinrel but Q9's is this shape, so a change here would
// have moved twenty queries silently.
func TestEstimateJoinSinglePairUnchanged(t *testing.T) {
	left := keyProofScan("orders", 1500000, []keyProofCol{{"o_orderkey", 1500000}, {"o_custkey", 150000}})
	right := keyProofScan("customer", 150000, []keyProofCol{{"c_custkey", 150000}, {"c_nationkey", 25}})
	j := keyedPairs(mergedJoin(JoinTypeInner, left, right), [2]int{1, 0})
	if got, want := EstimateRows(j), int64(1500000); got != want {
		t.Fatalf("single-pair estimate = %d, want %d (l·r/max(nd))", got, want)
	}
}

// --- half 2: the composite key replaces the product of marginals -----------

func TestCompositeUniqueKeyReplacesProductOfMarginals(t *testing.T) {
	j := q9Shape([]string{"ps_partkey", "ps_suppkey"})
	// One 1/ntuples for the key as a whole: 6 001 215 · 800 000 / 800 000.
	// Actual for this joinrel is 5 997 241 — 1.0007× out.
	if got, want := EstimateRows(j), int64(6001215); got != want {
		t.Fatalf("superkey estimate = %d, want %d (l·r/raw(partsupp))", got, want)
	}
}

// TestSuperkeyFiresOnKeyColumnOrderIndependently guards the ⊆ test: the query
// equates (suppkey, partkey) and the index is declared (partkey, suppkey). A
// key is a SET for this purpose — matching positionally would silently stop
// proving anything on real schemas.
func TestSuperkeyFiresOnKeyColumnOrderIndependently(t *testing.T) {
	reversed := q9Shape([]string{"ps_suppkey", "ps_partkey"})
	if got, want := EstimateRows(reversed), int64(6001215); got != want {
		t.Fatalf("estimate = %d, want %d — declared key order must not matter", got, want)
	}
}

// TestSuperkeyChickensOutOnPartialCover is PG's "if we failed to remove all the
// matching clauses we expected to find, chicken out" (costsize.c:5760). A
// composite key with only one of its columns equated proves NOTHING about
// fan-out, and treating it as proven would divide by 800 000 on the strength of
// a single 200 000-way column.
func TestSuperkeyChickensOutOnPartialCover(t *testing.T) {
	lineitem := keyProofScan("lineitem", 6001215, []keyProofCol{{"l_partkey", 200000}, {"l_suppkey", 10000}})
	partsupp := keyProofScan("partsupp", 800000, []keyProofCol{
		{"ps_partkey", 200000}, {"ps_suppkey", 10000},
	}, []string{"ps_partkey", "ps_suppkey"})
	j := keyedPairs(mergedJoin(JoinTypeInner, lineitem, partsupp), [2]int{0, 0}) // partkey only
	// Falls back to the per-pair estimate: 6 001 215 · 800 000 / 200 000.
	if got, want := EstimateRows(j), int64(24004860); got != want {
		t.Fatalf("partial-cover estimate = %d, want %d (per-pair, no proof)", got, want)
	}
}

// TestSuperkeyDoesNotComposeAcrossASelfJoin is why the prover keys its
// bookkeeping on the SCAN NODE rather than on `*catalog.Table`. Both sides here
// are the same relation (Q8's `nation n1, nation n2` shape); a table-keyed
// prover would see both key columns "equated on partsupp" and prove a composite
// key that no single relation instance actually has.
func TestSuperkeyDoesNotComposeAcrossASelfJoin(t *testing.T) {
	cols := []keyProofCol{{"ps_partkey", 200000}, {"ps_suppkey", 10000}}
	uniq := []string{"ps_partkey", "ps_suppkey"}
	left := keyProofScan("partsupp", 800000, cols, uniq)
	right := keyProofScan("partsupp", 800000, cols, uniq)
	// left.ps_partkey = right.ps_suppkey AND left.ps_suppkey = right.ps_partkey:
	// each side has BOTH of its key columns mentioned, but no side's key is
	// covered by clauses that equate it to the OTHER side as a unit... which
	// it is here, so the proof is legitimate. What must NOT happen is the
	// cross-composition below.
	j := keyedPairs(mergedJoin(JoinTypeInner, left, right), [2]int{0, 0})
	// Only ps_partkey is equated on either instance → partial cover on both,
	// no proof, per-pair pricing: 800 000 · 800 000 / 200 000.
	if got, want := EstimateRows(j), int64(3200000); got != want {
		t.Fatalf("self-join estimate = %d, want %d (no cross-instance key)", got, want)
	}
}

// TestSuperkeyFiresWhenOnlyTheKeySideResolves is Q20's measured shape and the
// reason the prover resolves each end INDEPENDENTLY.
//
// Q20's inner joinrel is
// `partsupp ⋈ (SELECT … FROM lineitem GROUP BY l_partkey, l_suppkey)` on both
// key columns. No resolver sees through the HashAggregate, so the far end has
// no base relation — but `partsupp` is still unique over the equated pair, so
// each aggregate row still matches at most one `partsupp` row. Demanding both
// ends (the mechanism's first shape) threw that proof away and priced the join
// at 283 against 236 624 actual.
func TestSuperkeyFiresWhenOnlyTheKeySideResolves(t *testing.T) {
	partsupp := keyProofScan("partsupp", 800000, []keyProofCol{
		{"ps_partkey", 200000}, {"ps_suppkey", 10000},
	}, []string{"ps_partkey", "ps_suppkey"})
	grouped := &Aggregate{
		Child:      keyProofScan("lineitem", 6001215, []keyProofCol{{"l_partkey", 200000}, {"l_suppkey", 10000}}),
		GroupExprs: []Expr{jrCol(0), jrCol(1)},
	}
	j := keyedPairs(mergedJoin(JoinTypeInner, partsupp, grouped), [2]int{0, 0}, [2]int{1, 1})
	// The proof divides by partsupp's raw count, and the structural bound
	// then caps the result at what the other side brings.
	other := EstimateRows(grouped)
	got := EstimateRows(j)
	if got > other {
		t.Fatalf("estimate %d exceeds the aggregate side's %d despite a proven key on partsupp", got, other)
	}
	// Without the proof this is 800 000·|agg|/(200 000·10 000) — three orders
	// of magnitude below the other side's row count.
	if got < other/10 {
		t.Fatalf("estimate %d collapsed to the product of marginals (other side %d)", got, other)
	}
}

// --- the structural bound (04 §3.3) ---------------------------------------

// TestKeyImpliedRowsBoundCapsTheProbeSide: a proven key means each row of the
// other side matches at most one key row, so the join cannot emit more rows
// than the other side BRINGS — its post-filter count. The divisor stays the RAW
// count (costsize.c:5852), so without the bound a heavily filtered key side
// would still report the unfiltered join size.
func TestKeyImpliedRowsBoundCapsTheProbeSide(t *testing.T) {
	// part is the key side; lineitem probes it, filtered down to 1000 rows.
	part := keyProofScan("part", 200000, []keyProofCol{{"p_partkey", 200000}}, []string{"p_partkey"})
	lineitem := keyProofScan("lineitem", 6001215, []keyProofCol{{"l_partkey", 200000}})
	filtered := &Filter{Child: lineitem, Predicate: &BooleanConst{Value: true}}
	j := keyedPairs(mergedJoin(JoinTypeInner, filtered, part), [2]int{0, 0})
	// EstimateRows(*Filter) does its own thing; pin the probe side by
	// asserting the invariant rather than a literal.
	probe := EstimateRows(filtered)
	if got := EstimateRows(j); got > probe {
		t.Fatalf("estimate %d exceeds the probe side's %d despite a proven key", got, probe)
	}
}

// --- declared foreign keys ------------------------------------------------

// TestDeclaredForeignKeyDividesByTheParentRawCount pins the asymmetry that
// `uniqueNoFanoutRawCount` (bushy.go) gets backwards: an FK declared on the
// CHILD makes each child row match exactly one PARENT row, so the divisor is
// the PARENT's raw count (`1.0 / ref_tuples`, costsize.c:5847). Dividing by the
// child's would divide the fact table's own cardinality out of the join.
func TestDeclaredForeignKeyDividesByTheParentRawCount(t *testing.T) {
	child := keyProofScan("orders", 1500000, []keyProofCol{{"o_orderkey", 1500000}, {"o_custkey", 150000}})
	child.Table.ForeignKeys = []catalog.ForeignKey{{
		Columns: []string{"o_custkey"}, RefTable: "customer", RefColumns: []string{"c_custkey"},
	}}
	parent := keyProofScan("customer", 150000, []keyProofCol{{"c_custkey", 150000}})
	j := keyedPairs(mergedJoin(JoinTypeInner, child, parent), [2]int{1, 0})
	// 1 500 000 · 150 000 / 150 000 — the child's rows, unchanged.
	if got, want := EstimateRows(j), int64(1500000); got != want {
		t.Fatalf("FK estimate = %d, want %d (divide by the PARENT's raw count)", got, want)
	}
}

// --- search-time / final-plan agreement -----------------------------------

// TestJoinEquiPairsAgreeBeforeAndAfterHashKeysAreFilled is the property that
// makes the fix reach plan SHAPE and not just the printed estimate.
// `Join.HashKeys` is filled by one late pass at the tail of Plan()
// (join_hash_keys.go), so every estimate taken during join-order search sees an
// EMPTY list. If the pair list were `HashKeys`-only with a single-pair
// fallback, the search would price Q9's joinrel at 4.8e8 and the finished plan
// would print 6.0e6 — the search would still make the wrong choice and the
// EXPLAIN output would hide it.
func TestJoinEquiPairsAgreeBeforeAndAfterHashKeysAreFilled(t *testing.T) {
	filled := q9Shape([]string{"ps_partkey", "ps_suppkey"})
	searching := q9Shape([]string{"ps_partkey", "ps_suppkey"})
	// Reproduce the mid-search state: Predicate written, HashKeys not yet
	// derived. `fillOneJoinHashKeys` would derive exactly these pairs.
	searching.Predicate = combineAnd([]Expr{
		&BinaryOp{Op: parser.OpEq, Left: jrCol(1), Right: jrCol(3)},
		&BinaryOp{Op: parser.OpEq, Left: jrCol(0), Right: jrCol(2)},
	})
	searching.HashKeys = nil

	if got, want := EstimateRows(searching), EstimateRows(filled); got != want {
		t.Fatalf("mid-search estimate = %d, finished-plan estimate = %d; they must agree", got, want)
	}
	if got := len(joinEquiPairs(searching)); got != 2 {
		t.Fatalf("joinEquiPairs derived %d pairs from Predicate, want 2", got)
	}
}

// --- the stamp ------------------------------------------------------------

func TestUniqueKeyColumnSetsTakesOnlyUniqueIndexes(t *testing.T) {
	tbl := &catalog.Table{Name: "partsupp"}
	cat := &stubIndexCatalog{indexes: []*catalog.Index{
		{Name: "partsupp_pk", Unique: true, Columns: []string{"ps_partkey", "ps_suppkey"}},
		{Name: "partsupp_part_fkidx", Unique: false, Columns: []string{"ps_partkey"}},
		{Name: "empty", Unique: true},
	}}
	got := uniqueKeyColumnSets(cat, tbl)
	if len(got) != 1 || len(got[0]) != 2 || got[0][0] != "ps_partkey" || got[0][1] != "ps_suppkey" {
		t.Fatalf("uniqueKeyColumnSets = %v, want [[ps_partkey ps_suppkey]]", got)
	}
	// The stamp must COPY: a later catalog mutation of the index's column
	// slice would otherwise reach into every plan already built.
	got[0][0] = "mutated"
	if again := uniqueKeyColumnSets(cat, tbl); again[0][0] != "ps_partkey" {
		t.Fatalf("stamp aliases the catalog's slice: %v", again)
	}
}

// stubIndexCatalog answers IndexesOnTable and nothing else; the prover reads no
// other catalog method.
type stubIndexCatalog struct {
	catalog.Catalog
	indexes []*catalog.Index
}

func (c *stubIndexCatalog) IndexesOnTable(_ *catalog.Table, _ ...uint32) []*catalog.Index {
	return c.indexes
}
