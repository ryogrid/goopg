package executor

// M0127-P0.3 — the single-map hash build (design leftdeep-joins/05 §4, stage
// E3).
//
// The build used to populate map[string][]Row and map[int64][]Row at the same
// time and let lazyHashFinalize keep whichever survived, so an int-keyed join
// held two complete copies of its build side at the exact moment peak memory
// mattered. The representation is now chosen before the first row arrives, from
// the plan's key types, and exactly one map is built.
//
// Two behaviours that change here need pinning, because neither shows up as a
// build-side row count:
//
//   - Semi/Anti builds are no longer excluded from the int64 lane. They were
//     excluded only because they never ran the finalize step that committed the
//     choice; with the choice made up front there is nothing to opt out of, and
//     the risk is that a semi build silently populates the map the probe does
//     not read (symptom: EXISTS returns zero rows).
//   - demoteIntHash is the safety net for an integer-typed column that yields a
//     datum outside int64. An int64-only table cannot hold such a key, so the
//     build must fall back to the string map WITHOUT losing or re-keying the
//     rows already inserted.

import (
	"testing"

	"github.com/goopg/goopg/internal/planner"
)

// assertIntBuildTable is assertBuildTable's int64-map twin.
func assertIntBuildTable(t *testing.T, hash map[int64][]Row, payloadCol int) {
	t.Helper()
	want := map[int64]string{1: "one", 2: "two", 3: "three"}
	if len(hash) != len(want) {
		t.Fatalf("int hash table has %d keys, want %d", len(hash), len(want))
	}
	for k, payload := range want {
		got := hash[k]
		if len(got) != 1 {
			t.Fatalf("key %d: %d rows, want 1", k, len(got))
		}
		if v := got[0][payloadCol].StringValue(); v != payload {
			t.Fatalf("key %d: payload %q, want %q", k, v, payload)
		}
	}
}

// TestBuildLoopBuildsOnlyTheSelectedMap pins the whole point of P0.3: with the
// int64 representation selected, the string map is never allocated (and vice
// versa). A test that only checked "the rows are findable" would still pass with
// both maps populated.
func TestBuildLoopBuildsOnlyTheSelectedMap(t *testing.T) {
	const leftWidth = 4

	for _, tc := range []struct {
		name  string
		isInt bool
	}{
		{"int64 selected", true},
		{"string selected", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			child := &bufferReuseOp{rows: buildProbeRows()}
			o := &joinOp{
				plan: &planner.Join{
					Type:     planner.JoinTypeInner,
					Algo:     planner.JoinAlgoHash,
					RightKey: &planner.ColumnRef{Index: leftWidth},
				},
				right:         child,
				lazyRW:        2,
				lazyHashIsInt: tc.isInt,
			}
			if err := child.Open(nil); err != nil {
				t.Fatalf("open child: %v", err)
			}
			if err := o.buildLoopRight(nil, leftWidth); err != nil {
				t.Fatalf("buildLoopRight: %v", err)
			}
			if tc.isInt {
				assertIntBuildTable(t, o.lazyIntHash, 1)
				if o.lazyHash != nil {
					t.Fatalf("string map allocated (%d keys) alongside the int map — "+
						"the dual-map build is back", len(o.lazyHash))
				}
			} else {
				assertBuildTable(t, o.lazyHash, 1)
				if o.lazyIntHash != nil {
					t.Fatalf("int map allocated (%d keys) alongside the string map",
						len(o.lazyIntHash))
				}
			}
		})
	}
}

// TestBuildLoopSemiUsesIntMap covers the lane that used to be excluded from the
// int64 fast path by an explicit JoinTypeInner gate.
func TestBuildLoopSemiUsesIntMap(t *testing.T) {
	const leftWidth = 4

	for _, jt := range []planner.JoinType{planner.JoinTypeSemi, planner.JoinTypeAnti} {
		child := &bufferReuseOp{rows: buildProbeRows()}
		o := &joinOp{
			plan: &planner.Join{
				Type:     jt,
				Algo:     planner.JoinAlgoHash,
				RightKey: &planner.ColumnRef{Index: leftWidth},
			},
			right:         child,
			lazyRW:        2,
			lazyHashIsInt: true,
		}
		if err := child.Open(nil); err != nil {
			t.Fatalf("open child: %v", err)
		}
		if err := o.buildLoopRight(nil, leftWidth); err != nil {
			t.Fatalf("buildLoopRight: %v", err)
		}
		assertIntBuildTable(t, o.lazyIntHash, 1)
		if o.lazyHash != nil {
			t.Fatalf("%v: string map allocated alongside the int map", jt)
		}
	}
}

// TestDemoteIntHashKeepsEveryRow drives the safety net directly: rows inserted
// under the int64 representation must end up under the SAME canonical string
// keys as the rows inserted after the demotion, or an equi-join silently splits
// one key into two buckets and drops the pairs.
func TestDemoteIntHashKeepsEveryRow(t *testing.T) {
	o := &joinOp{
		plan:          &planner.Join{Type: planner.JoinTypeInner, Algo: planner.JoinAlgoHash},
		lazyHashIsInt: true,
	}
	// Two int keys land in the int map...
	o.lazyHashInsertDatum(NewIntDatum(7), Row{NewIntDatum(7), NewStringDatum("a")})
	o.lazyHashInsertDatum(NewIntDatum(8), Row{NewIntDatum(8), NewStringDatum("b")})
	if len(o.lazyIntHash) != 2 {
		t.Fatalf("int map has %d keys before the demotion, want 2", len(o.lazyIntHash))
	}
	// ...then a key the int64 representation cannot hold forces the demotion.
	o.lazyHashInsertDatum(NewStringDatum("s"), Row{NewStringDatum("s"), NewStringDatum("c")})
	if o.lazyHashIsInt {
		t.Fatal("lazyHashIsInt still true after a non-int64 key")
	}
	if o.lazyIntHash != nil {
		t.Fatalf("int map survived the demotion with %d keys", len(o.lazyIntHash))
	}
	// A row inserted AFTER the demotion under a previously-int key must join
	// the rows that were re-keyed out of the int map.
	o.lazyHashInsertDatum(NewIntDatum(7), Row{NewIntDatum(7), NewStringDatum("d")})

	want := map[string]int{
		datumKey(NewIntDatum(7)):      2,
		datumKey(NewIntDatum(8)):      1,
		datumKey(NewStringDatum("s")): 1,
	}
	if len(o.lazyHash) != len(want) {
		t.Fatalf("string map has %d keys, want %d", len(o.lazyHash), len(want))
	}
	for k, n := range want {
		if got := len(o.lazyHash[k]); got != n {
			t.Errorf("key %q: %d rows, want %d — the demotion re-keyed rows it should have preserved", k, got, n)
		}
	}
	// The re-keyed rows must still carry their own payloads (a demotion that
	// appended the same slice twice would also produce the counts above).
	payloads := map[string]bool{}
	for _, r := range o.lazyHash[datumKey(NewIntDatum(7))] {
		payloads[r[1].StringValue()] = true
	}
	if !payloads["a"] || !payloads["d"] {
		t.Errorf("key 7 holds payloads %v, want both \"a\" and \"d\"", payloads)
	}
}
