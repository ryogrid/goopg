package executor

import (
	"fmt"
	"strings"
	"testing"

	"github.com/goopg/goopg/internal/optimizer"
	"github.com/goopg/goopg/internal/parser"
)

// TestCopyBulkExtensionMatchesPGRelpages pins M0146-0009h against PG 18.3:
// a COPY extends the relation through its BulkInsertState
// (RelationAddBlocks, hio.c), so the relation ends in the unused tail of its
// last bulk extension and counts more pages than its tuples fill.
//
// The same 20000 rows (an int and a 21-70 byte text), COPYed into PG 18.3:
//
//   - multi-insert (CIM_MULTI): 208 pages, 200 holding tuples — each flush
//     extends by what the rest of its batch needs, raised to what the COPY
//     has extended by so far, capped at 64;
//   - single-row (CIM_SINGLE, forced by a BEFORE ROW trigger): 256 pages,
//     200 holding tuples — every extension asks for one page, so the ramp is
//     1, 1, 2, 4, …, 64.
//
// A VACUUM afterwards keeps the first tail and truncates the second, as PG
// does. Before, goopg's COPY extended one page per row and the relation was
// exactly its 200 data pages.
func TestCopyBulkExtensionMatchesPGRelpages(t *testing.T) {
	ctx, cat, cleanup := newDDLFixture(t)
	defer cleanup()

	for _, q := range []string{
		"CREATE TABLE cbe_multi (a int, b text)",
		"CREATE TABLE cbe_single (a int, b text)",
		"CREATE FUNCTION cbe_noop() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RETURN NEW; END $$",
		"CREATE TRIGGER cbe_noop BEFORE INSERT ON cbe_single FOR EACH ROW EXECUTE FUNCTION cbe_noop()",
	} {
		if err := runDDL(t, ctx, q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	for _, tc := range []struct {
		table     string
		multi     bool
		wantPages int
		// after VACUUM: PG's should_attempt_truncation keeps the 8-page
		// tail (< 208/16) and gives back the 56-page one (>= 256/16).
		wantVacuumed int
	}{
		{"cbe_multi", true, 208, 208},
		{"cbe_single", false, 256, 200},
	} {
		tbl, ok := cat.LookupTable(parser.ObjectName{Name: tc.table})
		if !ok {
			t.Fatalf("%s not found", tc.table)
		}
		cf, err := NewCopyFromExecutor(ctx, &optimizer.Copy{
			Direction:   optimizer.CopyFrom,
			Table:       tbl,
			ColumnIndex: []int{0, 1},
			Endpoint:    optimizer.CopyEndpointStdin,
		})
		if err != nil {
			t.Fatal(err)
		}
		if cf.multiInsert != tc.multi {
			t.Fatalf("%s: multiInsert=%v, want %v", tc.table, cf.multiInsert, tc.multi)
		}
		for i := 1; i <= 20000; i++ {
			line := fmt.Sprintf("%d\t%s", i, strings.Repeat("x", 20+i%50))
			if err := cf.PushLine([]byte(line)); err != nil {
				t.Fatalf("%s line %d: %v", tc.table, i, err)
			}
		}
		if err := cf.Finish(); err != nil {
			t.Fatalf("%s Finish: %v", tc.table, err)
		}
		if cf.RowsInserted() != 20000 {
			t.Errorf("%s: RowsInserted=%d, want 20000", tc.table, cf.RowsInserted())
		}
		n, err := ctx.Pool.NBlocks(cat.RelFileNode(tbl))
		if err != nil {
			t.Fatal(err)
		}
		if int(n) != tc.wantPages {
			t.Errorf("%s: %d pages after COPY, want PG's %d", tc.table, n, tc.wantPages)
		}
		rows := runSQL(t, ctx, "SELECT count(*), sum(length(b)) FROM "+tc.table)
		if got := fmt.Sprint(rows[0]); !strings.Contains(got, "20000") {
			t.Errorf("%s: count/sum = %s, want 20000 rows", tc.table, got)
		}
		// pgbench -i's sequence: VACUUM truncates the tail only past
		// should_attempt_truncation's threshold, and the next insert still
		// finds a page that exists (the FSM forgets truncated blocks).
		runSQL(t, ctx, "VACUUM "+tc.table)
		if n2, _ := ctx.Pool.NBlocks(cat.RelFileNode(tbl)); int(n2) != tc.wantVacuumed {
			t.Errorf("%s: %d pages after VACUUM, want PG's %d", tc.table, n2, tc.wantVacuumed)
		}
		runSQL(t, ctx, "INSERT INTO "+tc.table+" VALUES (0, 'z')")
	}
}
