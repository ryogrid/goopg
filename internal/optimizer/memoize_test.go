package optimizer

// S7 — the maybeAttachMemoize insertion gate. The table below is the
// stage's acceptance contract: attach only for INNER/LEFT NLI joins
// whose probe keys are bare outer ColumnRefs with ANALYZE stats
// promising a repeating key stream (outerRows ≥ 1000, expected hit
// fraction ≥ 0.5); never for Semi/Anti (their early-out probes can
// never complete a cache entry) and never without stats.

import (
	"testing"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/parser"
)

// memoNLI builds an INNER-shaped NLI over outer(o_key) probing
// inner(i_key) through an index. keyND == 0 leaves the outer without
// stats (the common fresh-server case).
func memoNLI(t *testing.T, joinType JoinType, outerRows, keyND int64, unique bool) *NestedLoopIndexJoin {
	t.Helper()
	c := catalog.NewInMemory()
	outerTbl, err := c.CreateTable(parser.ObjectName{Name: "memo_o"}, []catalog.Column{
		{Name: "o_key", Type: catalog.Type{Name: "int4"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	innerTbl, err := c.CreateTable(parser.ObjectName{Name: "memo_i"}, []catalog.Column{
		{Name: "i_key", Type: catalog.Type{Name: "int4"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	idx, err := c.CreateIndex(parser.ObjectName{Name: "memo_i_key_idx"}, innerTbl,
		[]string{"i_key"}, unique, "btree", false)
	if err != nil {
		t.Fatal(err)
	}
	if keyND > 0 {
		outerTbl.Stats = &catalog.TableStats{RowCount: outerRows,
			Columns: []catalog.ColumnStats{{NDistinct: keyND}}}
	}
	outer := &SeqScan{Table: outerTbl, schema: Schema{{Name: "o_key"}}}
	return &NestedLoopIndexJoin{
		Type:  joinType,
		Outer: outer,
		Inner: &IndexScan{Table: innerTbl, Index: idx,
			Key: &ColumnRef{Index: 0, Name: "o_key"}},
	}
}

func TestMemoizeAttachGateTable(t *testing.T) {
	cases := []struct {
		name      string
		joinType  JoinType
		outerRows int64
		keyND     int64 // 0 = no outer stats
		unique    bool
		want      bool
	}{
		// The target shape: a big outer cycling few keys into a probe.
		{"inner-repeating-keys-attaches", JoinTypeInner, 4000, 10, true, true},
		{"left-repeating-keys-attaches", JoinTypeLeft, 4000, 10, false, true},
		// Semi/anti probes early-out — a complete entry is impossible.
		{"semi-never", JoinTypeSemi, 4000, 10, true, false},
		{"anti-never", JoinTypeAnti, 4000, 10, true, false},
		// Below the outer-rows floor the bookkeeping cannot pay off.
		{"small-outer-declines", JoinTypeInner, 500, 10, true, false},
		// hitFrac = 1 - 3000/4000 = 0.25 < 0.5.
		{"low-hit-fraction-declines", JoinTypeInner, 4000, 3000, true, false},
		// Stats are mandatory — a fresh server attaches nothing.
		{"no-stats-declines", JoinTypeInner, 0, 0, true, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			nli := memoNLI(t, tc.joinType, tc.outerRows, tc.keyND, tc.unique)
			maybeAttachMemoize(nli)
			if got := nli.InnerMemo != nil; got != tc.want {
				t.Fatalf("InnerMemo attached=%v, want %v", got, tc.want)
			}
			if tc.want {
				m := nli.InnerMemo
				if m.Child != nli.Inner {
					t.Fatalf("InnerMemo.Child must alias nli.Inner")
				}
				if m.SingleRow != tc.unique {
					t.Fatalf("SingleRow=%v, want %v (full-key unique probe)", m.SingleRow, tc.unique)
				}
				if m.EstEntries != tc.keyND {
					t.Fatalf("EstEntries=%d, want ndistinct=%d", m.EstEntries, tc.keyND)
				}
				if len(m.KeyExprs) != 1 {
					t.Fatalf("KeyExprs=%d, want 1", len(m.KeyExprs))
				}
			}
		})
	}
}

// TestMemoizeAttachKillSwitch — SetMemoizeEnabled(false) must suppress
// the attach even on the accepting shape (the enable_memoize bridge).
func TestMemoizeAttachKillSwitch(t *testing.T) {
	SetMemoizeEnabled(false)
	defer SetMemoizeEnabled(true)
	nli := memoNLI(t, JoinTypeInner, 4000, 10, true)
	maybeAttachMemoize(nli)
	if nli.InnerMemo != nil {
		t.Fatal("InnerMemo attached with the switch off")
	}
}

// TestMemoizeAttachNonColumnKey — a probe key that is not a bare outer
// ColumnRef (out-of-range Index models a post-remap mismatch) must
// decline: the ndistinct lookup would be undefined.
func TestMemoizeAttachNonColumnKey(t *testing.T) {
	nli := memoNLI(t, JoinTypeInner, 4000, 10, true)
	nli.Inner.Key = &ColumnRef{Index: 7, Name: "bogus"}
	maybeAttachMemoize(nli)
	if nli.InnerMemo != nil {
		t.Fatal("InnerMemo attached for an out-of-schema key")
	}
}
