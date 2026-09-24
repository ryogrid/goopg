package executor

import (
	"fmt"
	"testing"

	"github.com/goopg/goopg/internal/parser"
	"github.com/goopg/goopg/internal/storage"
)

func pageZeroFreeSpace(t *testing.T, ctx *Context, rel storage.RelFileNode) int {
	t.Helper()
	s, err := ctx.Pool.Pin(storage.BufferTag{Rel: rel, Block: 0})
	if err != nil {
		t.Fatal(err)
	}
	defer ctx.Pool.Unpin(s)
	s.RLock()
	defer s.RUnlock()
	return storage.PageGetHeapFreeSpace(s.Page())
}

// TestPruneOnAccessReclaimsDeadSpace pins heap_page_prune_opt on the read
// path (M0145-0008t): a SELECT that seq-scans a page short of free space
// whose deleted tuples are dead to all prunes it; with
// enable_opportunistic_prune off, or while another pin is held, it does not.
func TestPruneOnAccessReclaimsDeadSpace(t *testing.T) {
	for _, mode := range []string{"on", "off", "pinned"} {
		t.Run(mode, func(t *testing.T) {
			ctx, cat, cleanup := newHOTFixture(t)
			defer cleanup()
			ctx.EnableOpportunisticPrune = mode != "off"
			if err := runDDL(t, ctx, "CREATE TABLE t (id int, v text)"); err != nil {
				t.Fatal(err)
			}
			tbl, _ := cat.LookupTable(parser.ObjectName{Name: "t"})
			rel := ctx.Catalog.RelFileNode(tbl)
			last := 0
			for i := 1; i <= 1000; i++ {
				if err := runDDL(t, ctx, fmt.Sprintf("INSERT INTO t VALUES (%d, 'filler-filler-filler')", i)); err != nil {
					t.Fatal(err)
				}
				if n, _ := ctx.Pool.NBlocks(rel); n > 1 {
					last = i
					break
				}
			}
			if last == 0 {
				t.Fatal("page 0 never filled")
			}
			if err := runDDL(t, ctx, "DELETE FROM t WHERE id > 1"); err != nil {
				t.Fatal(err)
			}
			commitTx(t, ctx)
			beginTx(t, ctx)

			before := pageZeroFreeSpace(t, ctx, rel)
			var hold *storage.Slot
			if mode == "pinned" {
				var err error
				if hold, err = ctx.Pool.Pin(storage.BufferTag{Rel: rel, Block: 0}); err != nil {
					t.Fatal(err)
				}
			}
			rows := runQuery(t, ctx, "SELECT count(*) FROM t")
			if hold != nil {
				ctx.Pool.Unpin(hold)
			}
			if len(rows) != 1 || rows[0][0].Int != 1 {
				t.Fatalf("count = %+v, want 1", rows)
			}
			after := pageZeroFreeSpace(t, ctx, rel)
			if mode == "on" && after <= before {
				t.Fatalf("free space %d -> %d: the read did not prune", before, after)
			}
			if mode != "on" && after != before {
				t.Fatalf("free space %d -> %d: pruned with mode %s", before, after, mode)
			}
		})
	}
}
