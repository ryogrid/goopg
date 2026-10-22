package executor

import (
	"strings"
	"testing"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/optimizer"
)

// TestSubqueryInListMetaIsUniformAndReused pins M0122-0012 slice 65: a
// subquery IN's per-item metadata (the bpchar flag of the inner plan's first
// output column, no bare literals) is built once per InExpr and resliced to
// each row's result length instead of being allocated per row. The prefix
// must carry the uniform flag at every length, grow when a longer result
// arrives, and be shared across calls of the same length.
func TestSubqueryInListMetaIsUniformAndReused(t *testing.T) {
	for _, typ := range []catalog.Type{{Name: "bpchar", Args: []int64{5}}, {Name: "text"}} {
		tbl := &catalog.Table{Name: "inner", Columns: []catalog.Column{{Name: "c", Type: typ}}}
		x := &optimizer.InExpr{Plan: optimizer.SeqScanWithSchemaForTest(tbl, optimizer.Schema{{Name: "c", Type: typ}})}
		if out := x.Plan.Output(); len(out) != 1 {
			t.Fatalf("fixture plan exposes %d columns", len(out))
		}
		want := int64(0)
		if typ.Name == "bpchar" {
			want = 1
		}
		ctx := NewContext()
		var first []int64
		for _, n := range []int{3, 3, 10, 2, 0} {
			bp, lit := subqueryInListMeta(x, n, ctx)
			if len(bp) != n || len(lit) != n {
				t.Fatalf("%s n=%d: lengths %d/%d", typ.Name, n, len(bp), len(lit))
			}
			for i := 0; i < n; i++ {
				if bp[i] != want || lit[i] {
					t.Fatalf("%s n=%d item %d: bp=%d lit=%v, want %d/false", typ.Name, n, i, bp[i], lit[i], want)
				}
			}
			if n == 3 && first == nil {
				first = bp
			} else if n == 3 && &bp[0] != &first[0] {
				t.Fatalf("%s: a second call of the same length allocated afresh", typ.Name)
			}
		}
		// Without a Context the metadata is built per call, as before.
		if bp, _ := subqueryInListMeta(x, 4, nil); len(bp) != 4 || bp[0] != want {
			t.Fatalf("%s: no-context metadata %v", typ.Name, bp)
		}
	}
}

// TestInSubqueryBpcharSemantics: an IN subquery over char(n) and text
// columns still applies the blank rule the metadata encodes — a char(5)
// side compares blank-insensitively, a text side byte-exact — row after row
// (the metadata is now shared across rows).
func TestInSubqueryBpcharSemantics(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()
	for _, sql := range []string{
		"CREATE TABLE io (id int, c char(5), t text)",
		"CREATE TABLE ii (c char(5), t text)",
		"INSERT INTO io VALUES (1, 'ab', 'ab'), (2, 'ab  ', 'ab  '), (3, 'zz', 'zz'), (4, NULL, NULL), (5, 'x', 'x ')",
		"INSERT INTO ii VALUES ('ab', 'ab'), ('x', 'x'), ('q', 'q ')",
	} {
		if err := runDDL(t, ctx, sql); err != nil {
			t.Fatalf("%s: %v", sql, err)
		}
	}
	got := func(q string) string {
		var out []string
		for _, r := range runQuery(t, ctx, q) {
			var cells []string
			for _, d := range r {
				cells = append(cells, d.Format())
			}
			out = append(out, strings.Join(cells, ","))
		}
		return strings.Join(out, " ")
	}
	cases := map[string]string{
		// char = char: blank-insensitive.
		"SELECT id, c IN (SELECT c FROM ii) FROM io ORDER BY id": "1,t 2,t 3,f 4, 5,t",
		// text = text: byte-exact ('ab  ' and 'x ' do not match).
		"SELECT id, t IN (SELECT t FROM ii) FROM io ORDER BY id": "1,t 2,f 3,f 4, 5,f",
	}
	for q, want := range cases {
		if g := got(q); g != want {
			t.Fatalf("%s:\n got %s\nwant %s", q, g, want)
		}
	}
}
