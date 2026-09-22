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
// SUBSET. The script's second section builds a dummy foreign-data wrapper,
// three servers and two foreign tables, then asserts that
// `pg_dump --include-foreign-data=s0` FAILS with `foreign-data wrapper "dummy"
// has no handler`. That half is NOT ported, because goopg diverges: it models
// the objects correctly (`relkind='f'`, a `pg_foreign_table` row, `fdwhandler=0`)
// but a SELECT from such a foreign table returns zero rows where PostgreSQL
// raises the no-handler error, so the pg_dump run exits 0 instead of failing.
// Porting it would assert goopg's current behaviour rather than upstream's.
// The divergence is recorded in the deferral ledger (2026-09-22, M0122-0015)
// with its resume point.
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
