package postmaster

// Restart durability for user tables created under a distinct-dbOid database
// (M0122-0007 4e follow-up 39, design 0122-0018-per-database-catalog-
// namespace.md).
//
// Before follow-up 39, syncTableToCatalogHeap pinned every user table's
// pg_class/pg_attribute rows to DefaultDBOid's catalog heap and the startup
// loader registered everything it found back into the DefaultDBOid namespace:
// a table created under `CREATE DATABASE db1` survived a restart only as a
// ghost in the postgres namespace (with its data unreachable, since the
// reloaded catalog.Table lost the DBOid that routes reads to
// base/<dbOid>/<relOid>), and vanished from db1 entirely — a data-loss-shaped
// divergence from PostgreSQL's per-database catalog layout. These tests pin
// the fixed behavior end-to-end over the real wire protocol + a real data
// directory round-trip.

import (
	"context"
	"database/sql"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/goopg/goopg/internal/initdb"

	_ "github.com/lib/pq"
)

// dbidRestartServer bundles one server instance over a real initdb data dir,
// mirroring retTestServer (restart_after_retention_test.go) but with
// Config.WAL wired: the CREATE DATABASE bypass only survives a restart when
// its WAL record is appended, and syncTableToCatalogHeap's heap pages reach
// disk via WAL replay on the next Open.
type dbidRestartServer struct {
	rt     *initdb.Runtime
	srv    *Server
	addr   string
	cancel context.CancelFunc
	done   chan error
}

func startDBIDRestartServer(t *testing.T, dir string) *dbidRestartServer {
	t.Helper()
	rt, err := initdb.Open(initdb.OpenOptions{
		DataDir:   dir,
		PoolSlots: 128,
	})
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
	})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- srv.Run(ctx) }()
	<-srv.Ready()
	return &dbidRestartServer{rt: rt, srv: srv, addr: srv.Addr().String(), cancel: cancel, done: done}
}

func (s *dbidRestartServer) close(t *testing.T) {
	t.Helper()
	s.cancel()
	select {
	case <-s.done:
	case <-time.After(10 * time.Second):
		t.Error("server did not stop within 10s")
	}
	if err := s.rt.Close(); err != nil {
		t.Logf("runtime close: %v", err)
	}
}

func (s *dbidRestartServer) open(t *testing.T, dbname string) *sql.DB {
	t.Helper()
	dsn := "host=" + addrHost(s.addr) + " port=" + addrPort(s.addr) +
		" user=postgres dbname=" + dbname + " sslmode=disable"
	db, err := sql.Open("postgres", dsn)
	if err != nil {
		t.Fatalf("open %s: %v", dbname, err)
	}
	return db
}

// TestDistinctDatabaseTableSurvivesRestartInOwnNamespace: a table created
// under a distinct-dbOid database reloads, after a full data-dir round trip,
// into that database's OWN namespace with its rows intact — and does NOT
// leak into the postgres namespace.
func TestDistinctDatabaseTableSurvivesRestartInOwnNamespace(t *testing.T) {
	dir := t.TempDir()
	if err := initdb.Init(initdb.Options{DataDir: dir}); err != nil {
		t.Fatalf("initdb.Init: %v", err)
	}

	ctx := context.Background()
	s1 := startDBIDRestartServer(t, dir)

	pg := s1.open(t, "postgres")
	if _, err := pg.ExecContext(ctx, "CREATE DATABASE dbid_restart_db"); err != nil {
		pg.Close()
		s1.close(t)
		t.Fatalf("CREATE DATABASE: %v", err)
	}
	pg.Close()

	db1 := s1.open(t, "dbid_restart_db")
	for _, stmt := range []string{
		"CREATE TABLE t_survivor(a int, b text)",
		"INSERT INTO t_survivor VALUES (1,'x'),(2,'y')",
		// A dropped sibling proves the xmax stamp routes to the same
		// per-database heap the insert went to (tableCatalogDBOids):
		// a stamp that misses would resurrect t_dropped after restart.
		"CREATE TABLE t_dropped(c int)",
		"DROP TABLE t_dropped",
	} {
		if _, err := db1.ExecContext(ctx, stmt); err != nil {
			db1.Close()
			s1.close(t)
			t.Fatalf("%s: %v", stmt, err)
		}
	}
	db1.Close()
	s1.close(t)

	// Full restart from the same data dir.
	s2 := startDBIDRestartServer(t, dir)
	defer s2.close(t)

	db1 = s2.open(t, "dbid_restart_db")
	defer db1.Close()

	rows, err := db1.QueryContext(ctx, "SELECT a, b FROM t_survivor ORDER BY a")
	if err != nil {
		t.Fatalf("post-restart SELECT from own database: %v", err)
	}
	var got []struct {
		a int
		b string
	}
	for rows.Next() {
		var r struct {
			a int
			b string
		}
		if err := rows.Scan(&r.a, &r.b); err != nil {
			t.Fatalf("scan: %v", err)
		}
		got = append(got, r)
	}
	rows.Close()
	if len(got) != 2 || got[0].a != 1 || got[0].b != "x" || got[1].a != 2 || got[1].b != "y" {
		t.Fatalf("post-restart rows = %+v, want [(1,x) (2,y)]", got)
	}

	// The dropped sibling must stay dropped.
	if _, err := db1.QueryContext(ctx, "SELECT * FROM t_dropped"); err == nil {
		t.Fatalf("t_dropped resurrected after restart")
	}

	// And nothing leaked into the postgres namespace (the pre-fix failure
	// mode: the reloaded table registered under DefaultDBOid).
	pg = s2.open(t, "postgres")
	defer pg.Close()
	var leaked int
	if err := pg.QueryRowContext(ctx,
		"SELECT count(*) FROM pg_class WHERE relname IN ('t_survivor','t_dropped')").Scan(&leaked); err != nil {
		t.Fatalf("pg_class leak probe: %v", err)
	}
	if leaked != 0 {
		t.Fatalf("distinct-database tables leaked into postgres pg_class (count=%d, want 0)", leaked)
	}
}
