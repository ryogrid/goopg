package executor

import (
	"fmt"
	"math/rand"
	"strings"
	"testing"

	"github.com/goopg/goopg/internal/optimizer"
)

// M0122-0012 slice 74 guards: a presorted, partitioned window consumes its
// input one partition at a time (nodeWindowAgg.c spool_tuples), so a LIMIT
// above stops the input early, and every window function answers exactly as
// the whole-input evaluation does.

func TestWindowStreamingMatchesWholeInput(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()
	if err := runDDL(t, ctx, "CREATE TABLE ws (grp int, ord int, v int, n numeric)"); err != nil {
		t.Fatal(err)
	}
	r := rand.New(rand.NewSource(74))
	var vals []string
	for g := 0; g < 7; g++ {
		for o := 0; o < 1+r.Intn(20); o++ {
			v := fmt.Sprint(r.Intn(9) - 4)
			if r.Intn(7) == 0 {
				v = "NULL"
			}
			vals = append(vals, fmt.Sprintf("(%d, %d, %s, %d.%02d)", g, r.Intn(6), v, r.Intn(40)-20, r.Intn(100)))
		}
	}
	if err := runDDL(t, ctx, "INSERT INTO ws VALUES "+strings.Join(vals, ", ")); err != nil {
		t.Fatal(err)
	}
	over := "OVER (PARTITION BY grp ORDER BY ord, v, n)"
	queries := []string{
		"SELECT grp, ord, v, row_number() " + over + ", rank() " + over + ", dense_rank() " + over + " FROM ws ORDER BY 1, 2, 3, 4",
		"SELECT grp, ord, v, sum(v) " + over + ", count(*) " + over + ", avg(n) " + over + " FROM ws ORDER BY 1, 2, 3, 4",
		"SELECT grp, ord, v, lag(v) " + over + ", lead(n, 2) " + over + ", first_value(v) " + over + ", last_value(v) " + over + " FROM ws ORDER BY 1, 2, 3, 4",
		"SELECT grp, ord, v, ntile(3) " + over + ", percent_rank() " + over + ", cume_dist() " + over + " FROM ws ORDER BY 1, 2, 3, 4",
		"SELECT grp, sum(v) OVER (PARTITION BY grp) FROM ws ORDER BY 1, 2",
		"SELECT * FROM (SELECT grp, ord, v, rank() " + over + " AS rk FROM ws) s WHERE rk <= 2 ORDER BY 1, 2, 3, 4",
		"SELECT grp, ord, v, row_number() " + over + " FROM ws ORDER BY grp, ord, v, n LIMIT 5",
	}
	for _, q := range queries {
		windowStreamPartitions = false
		want := runQuery(t, ctx, q)
		windowStreamPartitions = true
		got := runQuery(t, ctx, q)
		if len(got) != len(want) {
			t.Fatalf("%s: %d rows streaming, %d whole-input", q, len(got), len(want))
		}
		for i := range want {
			for c := range want[i] {
				if g, w := got[i][c].Format(), want[i][c].Format(); g != w {
					t.Fatalf("%s row %d col %d: streaming %s, whole-input %s", q, i, c, g, w)
				}
			}
		}
	}
}

// countingRows is a rowsOp that counts the rows it hands out.
type countingRows struct {
	rowsOp
	pulled int
}

func (c *countingRows) Next() (TupleSlot, error) {
	s, err := c.rowsOp.Next()
	if err == nil {
		c.pulled++
	}
	return s, err
}

func TestWindowStreamingStopsAtThePartitionItNeeds(t *testing.T) {
	var rows []Row
	for p := 0; p < 3; p++ {
		for i := 0; i < 5; i++ {
			rows = append(rows, Row{NewIntDatum(int64(p)), NewIntDatum(int64(i))})
		}
	}
	child := &countingRows{rowsOp: rowsOp{rows: rows}}
	plan := &optimizer.WindowAgg{Presorted: true, PartitionBy: []optimizer.Expr{deformCol(0)}}
	w := newWindowOp(plan, child)
	if err := w.Open(&Context{}); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 5; i++ {
		s, err := w.Next()
		if err != nil || s.Row()[0].Int != 0 {
			t.Fatalf("row %d = %v (%v), want partition 0", i, s, err)
		}
	}
	// The first partition plus the one row that showed where it ended.
	if child.pulled != 6 {
		t.Fatalf("pulled %d input rows after the first partition, want 6", child.pulled)
	}
	n := 5
	for {
		_, err := w.Next()
		if err == EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		n++
	}
	if n != 15 || child.pulled != 15 {
		t.Fatalf("emitted %d, pulled %d; want 15 and 15", n, child.pulled)
	}
}
