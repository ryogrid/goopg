package postmaster

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/goopg/goopg/internal/initdb"
)

// startVMRestartServer is startDBIDRestartServer with the visibility map
// wired, as cmd/goopg wires it (cfg.VM = rt.VM): VACUUM sets no all-visible
// bits without it.
func startVMRestartServer(t *testing.T, dir string) *dbidRestartServer {
	t.Helper()
	rt, err := initdb.Open(initdb.OpenOptions{DataDir: dir, PoolSlots: 128})
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
	})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- srv.Run(ctx) }()
	<-srv.Ready()
	return &dbidRestartServer{rt: rt, srv: srv, addr: srv.Addr().String(), cancel: cancel, done: done}
}

// TestRelAllVisibleIsStoredLikePG pins M0146-0216's stored
// pg_class.relallvisible end to end, over the wire and across a restart.
//
// PG's planner reads the STORED relallvisible for allvisfrac (plancat.c
// estimate_rel_size). VACUUM and ANALYZE set it from the visibility map
// (vac_update_relstats), and pg_restore_relation_stats overwrites it. It is
// not the live map, so it goes stale between maintenance passes. goopg used to
// read the live map, so neither the staleness nor an imported value could
// reach the planner.
func TestRelAllVisibleIsStoredLikePG(t *testing.T) {
	dir := t.TempDir()
	if err := initdb.Init(initdb.Options{DataDir: dir}); err != nil {
		t.Fatalf("initdb.Init: %v", err)
	}
	ctx := context.Background()
	s1 := startVMRestartServer(t, dir)
	db := s1.open(t, "postgres")
	var vals []string
	for i := 0; i < 3000; i++ {
		vals = append(vals, fmt.Sprintf("(%d, 'payload %d padded to take some room on the page')", i, i))
	}
	for _, stmt := range []string{
		"CREATE TABLE t_av (a int PRIMARY KEY, b text)",
		"INSERT INTO t_av VALUES " + strings.Join(vals, ","),
		"VACUUM t_av",
	} {
		if _, err := db.ExecContext(ctx, stmt); err != nil {
			db.Close()
			s1.close(t)
			t.Fatalf("%s: %v", stmt[:min(len(stmt), 40)], err)
		}
	}
	const q = "SELECT relpages, relallvisible FROM pg_class WHERE relname = 't_av'"
	var pages, allvis int
	if err := db.QueryRowContext(ctx, q).Scan(&pages, &allvis); err != nil {
		t.Fatal(err)
	}
	if pages < 2 || allvis != pages {
		t.Fatalf("after VACUUM relpages=%d relallvisible=%d, want relallvisible == relpages >= 2", pages, allvis)
	}

	// An UPDATE clears the visibility-map bits of the pages it touches, but
	// the stored relallvisible stays as VACUUM left it until the next pass.
	if _, err := db.ExecContext(ctx, "UPDATE t_av SET b = b || 'x' WHERE a % 100 = 0"); err != nil {
		t.Fatal(err)
	}
	var stale int
	if err := db.QueryRowContext(ctx, q).Scan(&pages, &stale); err != nil {
		t.Fatal(err)
	}
	if stale != allvis {
		t.Fatalf("relallvisible after UPDATE = %d, want the stored %d (PG does not track the live map)", stale, allvis)
	}

	// An import sets it, and the value survives a restart.
	if _, err := db.ExecContext(ctx, "SELECT pg_restore_relation_stats('schemaname','public','relname','t_av','relallvisible',1::integer)"); err != nil {
		t.Fatal(err)
	}
	db.Close()
	s1.close(t)

	s2 := startVMRestartServer(t, dir)
	defer s2.close(t)
	db = s2.open(t, "postgres")
	defer db.Close()
	var after int
	if err := db.QueryRowContext(ctx, q).Scan(&pages, &after); err != nil {
		t.Fatal(err)
	}
	if after != 1 {
		t.Fatalf("relallvisible after restart = %d, want the imported 1", after)
	}
}
