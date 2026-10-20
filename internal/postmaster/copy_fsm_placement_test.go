package postmaster

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/goopg/goopg/internal/initdb"
)

// copyPlacementPGLayout is PG 18.3's heap layout for the COPY below, measured
// on a scratch PG (`SELECT (ctid::text::point)[0]::int, count(*) FROM t GROUP
// BY 1 ORDER BY 1`): rows per block, blocks 0..410 in order.
const copyPlacementPGLayout = "777777778778888888777777777877888888877777777778788888877777778778888888877777777778788888877777787777778888877777777777778888877777777777777888877777777777777888887777777777777888887777777777777788888777777777777788888777777777777788888777777777777778888877777777777778888877777777777788888877777777777788888887777777777777888887777777777777888887777777777777888887777777777777888888777777777777788887777777773"

// copyPlacementPGBackJumps are the rows PG put on an EARLIER page than the
// row before them, as id→ctid: each is a short row that went back through
// the FSM when the bulk extension's pages ran out.
const copyPlacementPGBackJumps = "71:(8,8) 292:(12,8) 585:(66,8) 587:(11,8) 1054:(47,8) 1987:(83,8) 2455:(286,8) 2925:(30,8)"

// TestCopyFromRefillsEarlierPagesThroughTheFSM pins M0146-0216's COPY heap
// placement against PG. RelationGetBufferForTuple (hio.c) with a bulk insert
// state uses the current page, then the bulk extension's next free page, and
// once those run out asks RecordAndGetPageWithFreeSpace for an earlier page
// with room before it extends. goopg's server-side COPY built its executor
// context without the free space map, so it never recorded a page it left and
// never found one: it filled the heap strictly forward, and every index built
// over the rows' TIDs split differently from PG's (TPC-DS store_sales_pkey
// 2567 pages vs PG 2569).
func TestCopyFromRefillsEarlierPagesThroughTheFSM(t *testing.T) {
	dir := t.TempDir()
	if err := initdb.Init(initdb.Options{DataDir: dir}); err != nil {
		t.Fatalf("initdb.Init: %v", err)
	}
	rt, err := initdb.Open(initdb.OpenOptions{DataDir: dir, PoolSlots: 256})
	if err != nil {
		t.Fatalf("initdb.Open: %v", err)
	}
	srv := New(Config{
		Address:          "127.0.0.1:0",
		Logger:           slog.New(slog.NewTextHandler(io.Discard, nil)),
		AcceptDeadline:   25 * time.Millisecond,
		HandshakeTimeout: 2 * time.Second,
		Catalog:          rt.Catalog,
		Pool:             rt.Pool,
		TxnMgr:           rt.TxnMgr,
		Checkpointer:     rt.Checkpointer,
		WAL:              rt.WAL,
		VM:               rt.VM,
		FSM:              rt.FSM,
	})
	runCtx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- srv.Run(runCtx) }()
	<-srv.Ready()
	s := &dbidRestartServer{rt: rt, srv: srv, addr: srv.Addr().String(), cancel: cancel, done: done}
	defer s.close(t)
	db := s.open(t, "postgres")
	defer db.Close()

	// 3000 rows of 800-1200 bytes: pages close with up to a row's worth of
	// room, which a later, shorter row can use.
	var b strings.Builder
	for i := 1; i <= 3000; i++ {
		fmt.Fprintf(&b, "%d\t%s\n", i, strings.Repeat("x", 800+(i*7919)%401))
	}
	tsv := filepath.Join(t.TempDir(), "probe.tsv")
	if err := os.WriteFile(tsv, []byte(b.String()), 0o600); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	// The table is created in its own transaction, so COPY may use the FSM
	// (copyfrom.c sets TABLE_INSERT_SKIP_FSM only for a relfilenode created
	// in the COPY's transaction).
	for _, stmt := range []string{
		"CREATE TABLE t (id int PRIMARY KEY, pad text)",
		"COPY t FROM '" + tsv + "'",
	} {
		if _, err := db.ExecContext(ctx, stmt); err != nil {
			t.Fatalf("%s: %v", stmt, err)
		}
	}

	rows, err := db.QueryContext(ctx, "SELECT (ctid::text::point)[0]::int, count(*) FROM t GROUP BY 1 ORDER BY 1")
	if err != nil {
		t.Fatal(err)
	}
	var layout strings.Builder
	next := 0
	for rows.Next() {
		var blk, n int
		if err := rows.Scan(&blk, &n); err != nil {
			t.Fatal(err)
		}
		if blk != next {
			t.Fatalf("block %d holds no row; PG's layout has no gap", next)
		}
		next++
		fmt.Fprintf(&layout, "%d", n)
	}
	rows.Close()
	if got := layout.String(); got != copyPlacementPGLayout {
		t.Errorf("rows per block differ from PG 18.3:\n got %s\nwant %s", got, copyPlacementPGLayout)
	}

	// The back-jumps are found here rather than with lag(ctid): goopg's
	// WindowAgg does not carry ctid (M0146-0236).
	rows, err = db.QueryContext(ctx, "SELECT id, ctid::text, (ctid::text::point)[0]::int, (ctid::text::point)[1]::int FROM t ORDER BY id")
	if err != nil {
		t.Fatal(err)
	}
	var jumps []string
	prevBlk, prevOff := -1, -1
	for rows.Next() {
		var id, blk, off int
		var ctid string
		if err := rows.Scan(&id, &ctid, &blk, &off); err != nil {
			t.Fatal(err)
		}
		if blk < prevBlk || (blk == prevBlk && off < prevOff) {
			jumps = append(jumps, fmt.Sprintf("%d:%s", id, ctid))
		}
		prevBlk, prevOff = blk, off
	}
	rows.Close()
	if got := strings.Join(jumps, " "); got != copyPlacementPGBackJumps {
		t.Errorf("rows placed on an earlier page differ from PG 18.3:\n got %q\nwant %q", got, copyPlacementPGBackJumps)
	}
}
