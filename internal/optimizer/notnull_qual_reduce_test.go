package optimizer

import (
	"testing"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/parser"
)

// predTabCatalog builds `pred_tab(a int NOT NULL, b int, c int NOT NULL)` —
// the exact shape `postgres/src/test/regress/sql/predicate.sql` uses, so the
// seven cases below are the resolved-plan twins of the seven EXPLAINs the
// M0134-0010 design doc cites (§2 table).
func predTabCatalog(t *testing.T) (catalog.Catalog, *catalog.Table) {
	t.Helper()
	c := catalog.NewInMemory()
	tbl, err := c.CreateTable(parser.ObjectName{Name: "pred_tab"}, []catalog.Column{
		{Name: "a", Type: catalog.Type{Name: "int4"}, NotNull: true},
		{Name: "b", Type: catalog.Type{Name: "int4"}},
		{Name: "c", Type: catalog.Type{Name: "int4"}, NotNull: true},
	})
	if err != nil {
		t.Fatal(err)
	}
	return c, tbl
}

// planPredTabWhere plans `SELECT * FROM pred_tab t WHERE <where>` and
// unwraps the top-level Project (SELECT * always projects — M0071-0009's
// identity Project) to return the node the WHERE-clause branch built.
func planPredTabWhere(t *testing.T, cat catalog.Catalog, where string) Node {
	t.Helper()
	node, err := Plan(parseOne(t, "SELECT * FROM pred_tab t WHERE "+where), cat)
	if err != nil {
		t.Fatalf("plan(%s): %v", where, err)
	}
	proj, ok := node.(*Project)
	if !ok {
		t.Fatalf("plan(%s) root = %T, want *Project", where, node)
	}
	return proj.Child
}

// TestReduceNotNullQuals_SevenShapes covers the seven predicate.sql shapes
// listed in docs/design/m0134-0010-notnull-qual-reduction.md §2: four that
// the pass changes, three that it must leave byte-unmodified (Filter kept).
func TestReduceNotNullQuals_SevenShapes(t *testing.T) {
	cat, tbl := predTabCatalog(t)

	t.Run("a IS NOT NULL -> bare scan, no Filter (restriction_is_always_true)", func(t *testing.T) {
		child := planPredTabWhere(t, cat, "t.a IS NOT NULL")
		seq, ok := child.(*SeqScan)
		if !ok {
			t.Fatalf("child = %T, want *SeqScan (Filter must be dropped entirely)", child)
		}
		if seq.Table != tbl {
			t.Fatalf("SeqScan.Table = %v, want %v", seq.Table, tbl)
		}
	})

	t.Run("b IS NOT NULL -> Filter kept (b is nullable)", func(t *testing.T) {
		child := planPredTabWhere(t, cat, "t.b IS NOT NULL")
		f, ok := child.(*Filter)
		if !ok {
			t.Fatalf("child = %T, want *Filter (b is nullable, must not be reduced)", child)
		}
		nt, ok := f.Predicate.(*IsNullExpr)
		if !ok || !nt.Negated {
			t.Fatalf("Filter.Predicate = %#v, want unmodified `b IS NOT NULL`", f.Predicate)
		}
		if _, ok := f.Child.(*SeqScan); !ok {
			t.Fatalf("Filter.Child = %T, want *SeqScan", f.Child)
		}
	})

	t.Run("a IS NULL -> Result{OneTimeFilter: false} (restriction_is_always_false)", func(t *testing.T) {
		child := planPredTabWhere(t, cat, "t.a IS NULL")
		res, ok := child.(*Result)
		if !ok {
			t.Fatalf("child = %T, want *Result", child)
		}
		bc, ok := res.OneTimeFilter.(*BooleanConst)
		if !ok || bc.Value != false {
			t.Fatalf("OneTimeFilter = %#v, want BooleanConst{Value: false}", res.OneTimeFilter)
		}
		// PG's plan is a CHILDLESS Result (predicate.out lines 34-40: 2
		// rows, no `->` line) — the now-unreachable scan must not be
		// attached as Child. Round 2 regression: it was wrongly kept.
		if res.Child != nil {
			t.Fatalf("Result.Child = %#v, want nil (PG's Result has no scan beneath it)", res.Child)
		}
		if len(res.Targets) != 3 {
			t.Fatalf("Result has %d targets, want 3 (identity pass-through, for Output()/row description)", len(res.Targets))
		}
	})

	t.Run("a IS NOT NULL OR b = 1 -> bare scan (ANY arm provably true)", func(t *testing.T) {
		child := planPredTabWhere(t, cat, "t.a IS NOT NULL OR t.b = 1")
		if _, ok := child.(*SeqScan); !ok {
			t.Fatalf("child = %T, want *SeqScan (whole OR dropped: a IS NOT NULL arm is always true)", child)
		}
	})

	t.Run("b IS NOT NULL OR a = 1 -> Filter kept, unmodified (b arm is not provable)", func(t *testing.T) {
		child := planPredTabWhere(t, cat, "t.b IS NOT NULL OR t.a = 1")
		f, ok := child.(*Filter)
		if !ok {
			t.Fatalf("child = %T, want *Filter", child)
		}
		bin, ok := f.Predicate.(*BinaryOp)
		if !ok || bin.Op != parser.OpOr {
			t.Fatalf("Filter.Predicate = %#v, want unmodified OR", f.Predicate)
		}
		// The `a = 1` arm (provably-false-or-true is not decidable for an
		// equality against a constant) must survive UNCHANGED — the
		// asymmetric rule never prunes individual disprovable arms.
		if _, ok := bin.Right.(*BinaryOp); !ok {
			t.Fatalf("OR right arm = %#v, want the unmodified `a = 1` BinaryOp", bin.Right)
		}
	})

	t.Run("a IS NULL OR c IS NULL -> Result{OneTimeFilter: false} (ALL arms provably false)", func(t *testing.T) {
		child := planPredTabWhere(t, cat, "t.a IS NULL OR t.c IS NULL")
		res, ok := child.(*Result)
		if !ok {
			t.Fatalf("child = %T, want *Result", child)
		}
		bc, ok := res.OneTimeFilter.(*BooleanConst)
		if !ok || bc.Value != false {
			t.Fatalf("OneTimeFilter = %#v, want BooleanConst{Value: false}", res.OneTimeFilter)
		}
		// PG's plan is a CHILDLESS Result (predicate.out lines 75-81: 2
		// rows, no `->` line).
		if res.Child != nil {
			t.Fatalf("Result.Child = %#v, want nil (PG's Result has no scan beneath it)", res.Child)
		}
	})

	t.Run("b IS NULL OR c IS NULL -> Filter kept, unmodified (b arm is not provable)", func(t *testing.T) {
		child := planPredTabWhere(t, cat, "t.b IS NULL OR t.c IS NULL")
		f, ok := child.(*Filter)
		if !ok {
			t.Fatalf("child = %T, want *Filter", child)
		}
		bin, ok := f.Predicate.(*BinaryOp)
		if !ok || bin.Op != parser.OpOr {
			t.Fatalf("Filter.Predicate = %#v, want unmodified OR", f.Predicate)
		}
		// The `c IS NULL` arm IS individually provably-false (c is NOT
		// NULL) but must survive UNCHANGED anyway — only ALL-false folds
		// the whole OR, and here the `b IS NULL` arm is not provable.
		rc, ok := bin.Right.(*IsNullExpr)
		if !ok || rc.Negated {
			t.Fatalf("OR right arm = %#v, want the unmodified `c IS NULL`", bin.Right)
		}
	})
}

// TestReduceNotNullQuals_RowValuedNeverFolded is the design doc §4 "argisrow"
// guard: goopg's *IsNullExpr has no dedicated row-operand flag at all — a
// row-valued IS NULL simply resolves its Operand to a *RowExpr, which
// exprIsNonNullable rejects structurally (it only recognises a bare
// *ColumnRef), so `(a, c) IS NULL` is never folded even though both a and c
// are individually NOT NULL columns of the same table.
func TestReduceNotNullQuals_RowValuedNeverFolded(t *testing.T) {
	cat, _ := predTabCatalog(t)
	child := planPredTabWhere(t, cat, "(t.a, t.c) IS NULL")
	f, ok := child.(*Filter)
	if !ok {
		t.Fatalf("child = %T, want *Filter (row-valued IS NULL must never fold)", child)
	}
	nt, ok := f.Predicate.(*IsNullExpr)
	if !ok || nt.Negated {
		t.Fatalf("Filter.Predicate = %#v, want unmodified `(a, c) IS NULL`", f.Predicate)
	}
	if _, ok := nt.Operand.(*RowExpr); !ok {
		t.Fatalf("IsNullExpr.Operand = %T, want *RowExpr — confirms the operand is row-valued, "+
			"not a bare ColumnRef, which is why exprIsNonNullable declines it structurally", nt.Operand)
	}
}

// TestReduceNotNullQuals_JoinQualUnaffected is the single-baserel gate's
// regression guard (acceptance criterion 2): a two-table query with an IS
// NULL qual on a NOT NULL column must NOT be reduced. Join-`ON`/WHERE quals
// are distributed to a per-relation Filter by a DIFFERENT pass
// (attachRelationLocalFilters) that this brief's hook never touches — join
// quals are category 2, deferred (design doc §5.1) because reducing them
// safely needs the outer-join-nullability primitive (§5.2) this slice does
// not have.
func TestReduceNotNullQuals_JoinQualUnaffected(t *testing.T) {
	cat, _ := predTabCatalog(t)
	if _, err := cat.CreateTable(parser.ObjectName{Name: "other"}, []catalog.Column{
		{Name: "x", Type: catalog.Type{Name: "int4"}},
	}); err != nil {
		t.Fatal(err)
	}
	node, err := Plan(parseOne(t, "SELECT * FROM pred_tab t, other o WHERE t.a IS NULL"), cat)
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	proj, ok := node.(*Project)
	if !ok {
		t.Fatalf("root = %T, want *Project", node)
	}
	join, ok := proj.Child.(*Join)
	if !ok {
		t.Fatalf("Project.Child = %T, want *Join", proj.Child)
	}
	f, ok := join.Left.(*Filter)
	if !ok {
		t.Fatalf("Join.Left = %T, want *Filter carrying the unreduced `a IS NULL` qual", join.Left)
	}
	nt, ok := f.Predicate.(*IsNullExpr)
	if !ok || nt.Negated {
		t.Fatalf("Join.Left Filter.Predicate = %#v, want unmodified `a IS NULL` — "+
			"the single-baserel gate must not have fired for a join query", f.Predicate)
	}
}
