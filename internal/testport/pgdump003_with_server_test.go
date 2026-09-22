package testport

import (
	"strings"
	"testing"
	"time"

	"github.com/goopg/goopg/internal/testutil/cluster"
	"github.com/goopg/goopg/internal/testutil/util"
)

// TestPort_PgDump003NewlineInDatabaseName ports the first half of
// postgres/src/bin/pg_dump/t/003_pg_dump_with_server.pl (M0122-0015).
//
// Upstream's first section is a SECURITY property, not a formatting one. A
// database name may legally contain a newline, and pg_dumpall builds a
// connection string per database and hands it to a shell-quoted `psql`
// invocation inside the dump it emits. If a newline survived that, everything
// after it would land on its own line of the generated script — outside the
// `--` comment that was supposed to contain it — and would execute on restore.
// PostgreSQL's guard is in `appendShellString` (src/fe_utils/string_utils.c),
// which refuses the argument outright.
//
// The test therefore asserts all three of upstream's checks, and each one
// matters separately:
//
//	ok(!$result, ...)                                  -> exit status is nonzero
//	like($stderr, qr/shell command argument contains a newline/) -> the guard
//	unlike($stdout, qr/^attack/m, "no comment escape") -> nothing broke out
//
// The third is the one that would catch a regression where goopg reported the
// name in some pre-escaped form that satisfied the guard while still splitting
// the emitted script.
//
// WHAT THIS PROVES ABOUT GOOPG, since the binary is upstream's: that goopg
// accepts `CREATE DATABASE "regress_\nattack"` and reports the name verbatim
// through pg_database, so the real pg_dumpall's protection engages against a
// goopg cluster exactly as it does against PostgreSQL. Measured 2026-09-22:
// goopg exits 1 with `shell command argument contains a newline or carriage
// return: "host=… dbname='regress_\nattack'"` and emits no `^attack` line.
//
// The script's second section is ported alongside this one as
// TestPort_PgDump003ForeignDataNoHandler, once the divergence it exposed was
// fixed (see that test).
func TestPort_PgDump003NewlineInDatabaseName(t *testing.T) {
	bin := clientToolBin(t, "pg_dumpall")
	if bin == "" {
		t.Skip("pg_dumpall not in PATH or postgres/local_install/bin")
	}
	c := newCluster(t, "pgdump003newline")
	mustInitStart(t, c)
	defer func() { _ = c.Stop(cluster.ShutdownImmediate) }()

	// Upstream: CREATE DATABASE "regress_\nattack". The newline is inside the
	// quoted identifier, so it is part of the name rather than a statement
	// separator.
	if err := runSQLSimple(t, c, "CREATE DATABASE \"regress_\nattack\""); err != nil {
		t.Fatalf("create database with newline in name: %v", err)
	}

	// --exclude-database=postgres mirrors upstream: it forces pg_dumpall to
	// iterate the OTHER databases, which is what reaches the offending name.
	res, err := util.RunCommand(util.CommandSpec{
		Name:    bin,
		Args:    []string{"--no-sync", "--exclude-database=postgres"},
		Env:     amcheckEnv(t, c),
		Timeout: 60 * time.Second,
	})
	if err != nil {
		t.Fatalf("run pg_dumpall: %v", err)
	}

	if res.ExitCode == 0 {
		t.Errorf("pg_dumpall exited 0 for a database name containing a newline; upstream requires a failure\nstdout=%s\nstderr=%s",
			res.Stdout, res.Stderr)
	}
	if !strings.Contains(res.Stderr, "shell command argument contains a newline") {
		t.Errorf("pg_dumpall stderr does not carry the upstream guard message\nstderr=%s", res.Stderr)
	}
	// "no comment escape": no emitted line may START with `attack`, which is
	// what the text after the newline would become if the name were
	// interpolated unguarded.
	for line := range strings.SplitSeq(res.Stdout, "\n") {
		if strings.HasPrefix(line, "attack") {
			t.Fatalf("pg_dumpall stdout has a line starting with %q — the newline escaped its comment\nfull stdout=%s",
				"attack", res.Stdout)
		}
	}
}

// TestPort_PgDump003ForeignDataNoHandler ports the second section of
// postgres/src/bin/pg_dump/t/003_pg_dump_with_server.pl (M0122-0015).
//
// Upstream builds a dummy foreign-data wrapper with NO HANDLER, three servers
// and two foreign tables, then asserts two things that only make sense
// together:
//
//	command_fails_like([pg_dump --include-foreign-data=s0 …],
//	    qr/foreign-data wrapper "dummy" has no handler\r?\npg_dump: detail: Query was: .*t0/)
//	command_ok([pg_dump --data-only --include-foreign-data=s2 …],
//	    "dump foreign server with no tables")
//
// The pair is the point. The first says dumping foreign DATA from a server
// whose wrapper cannot execute must fail, and fail while reading t0
// specifically. The second says the failure is about the table, not about
// --include-foreign-data itself: a server with no foreign tables dumps fine.
//
// WHAT THIS EXERCISES IN GOOPG. pg_dump reaches the failure by issuing
// `COPY (SELECT a FROM public.t0 ) TO stdout`, so the assertion lands on
// goopg's planner refusing to read a foreign table whose FDW has fdwhandler=0
// — upstream raises the same error from GetFdwRoutineByServerId
// (postgres/src/backend/foreign/foreign.c:403), and raises it at PLAN time,
// which is why EXPLAIN fails there too. Measured against PG 18.3 and goopg:
// both report SQLSTATE 55000 with the identical message text.
//
// Before that refusal existed, goopg returned zero rows here and this pg_dump
// run exited 0 — i.e. a user asking to dump foreign data got an empty dump
// instead of an error. That is what makes this port worth having rather than
// a formality.
func TestPort_PgDump003ForeignDataNoHandler(t *testing.T) {
	bin := clientToolBin(t, "pg_dump")
	if bin == "" {
		t.Skip("pg_dump not in PATH or postgres/local_install/bin")
	}
	c := newCluster(t, "pgdump003fdw")
	mustInitStart(t, c)
	defer func() { _ = c.Stop(cluster.ShutdownImmediate) }()

	for _, stmt := range []string{
		"CREATE FOREIGN DATA WRAPPER dummy",
		"CREATE SERVER s0 FOREIGN DATA WRAPPER dummy",
		"CREATE SERVER s1 FOREIGN DATA WRAPPER dummy",
		"CREATE SERVER s2 FOREIGN DATA WRAPPER dummy",
		"CREATE FOREIGN TABLE t0 (a int) SERVER s0",
		"CREATE FOREIGN TABLE t1 (a int) SERVER s1",
	} {
		if err := runSQLSimple(t, c, stmt); err != nil {
			t.Fatalf("%s: %v", stmt, err)
		}
	}

	// Upstream: correctly fails to dump a foreign table from a dummy FDW.
	res, err := util.RunCommand(util.CommandSpec{
		Name:    bin,
		Args:    []string{"--include-foreign-data=s0", "postgres"},
		Env:     amcheckEnv(t, c),
		Timeout: 60 * time.Second,
	})
	if err != nil {
		t.Fatalf("run pg_dump --include-foreign-data=s0: %v", err)
	}
	if res.ExitCode == 0 {
		t.Errorf("pg_dump --include-foreign-data=s0 exited 0; upstream requires a failure\nstdout=%s\nstderr=%s",
			res.Stdout, res.Stderr)
	}
	if !strings.Contains(res.Stderr, `foreign-data wrapper "dummy" has no handler`) {
		t.Errorf("pg_dump stderr lacks the upstream no-handler message\nstderr=%s", res.Stderr)
	}
	// Upstream's regex also pins that the failing query is the one reading t0,
	// which is what distinguishes "the scan refused" from "pg_dump gave up
	// somewhere else".
	if !strings.Contains(res.Stderr, "Query was:") || !strings.Contains(res.Stderr, "t0") {
		t.Errorf("pg_dump stderr does not attribute the failure to the query reading t0\nstderr=%s", res.Stderr)
	}

	// Upstream: dump foreign server with no tables. s2 has none, so this must
	// succeed — the refusal above must be about t0, not about the option.
	res2, err := util.RunCommand(util.CommandSpec{
		Name:    bin,
		Args:    []string{"--data-only", "--include-foreign-data=s2", "postgres"},
		Env:     amcheckEnv(t, c),
		Timeout: 60 * time.Second,
	})
	if err != nil {
		t.Fatalf("run pg_dump --include-foreign-data=s2: %v", err)
	}
	if res2.ExitCode != 0 {
		t.Errorf("pg_dump --data-only --include-foreign-data=s2 exited %d; a server with no foreign tables must dump cleanly\nstdout=%s\nstderr=%s",
			res2.ExitCode, res2.Stdout, res2.Stderr)
	}
}
