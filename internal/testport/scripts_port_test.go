package testport

// Ports of postgres/src/bin/scripts/t/*.pl into Go tests.
//
// Upstream suite: D-005 in docs/test-port/postgres-oracle-port-status.csv
// Milestone doc: docs/milestones/0060-postgres-oracle-test-port.md
//
// Each test corresponds to one upstream .pl file. Tests that require SQL
// features not yet implemented in goopg call t.Skip with the specific blocker
// so they are easy to unlock as features land.

import (
	"os/exec"
	"testing"

	"github.com/goopg/goopg/internal/testutil/cluster"
)

// TestPort_Scripts080PgIsready ports postgres/src/bin/scripts/t/080_pg_isready.pl.
//
// Upstream tests:
//   - pg_isready fails when no server is running
//   - pg_isready succeeds with server running
func TestPort_Scripts080PgIsready(t *testing.T) {
	if _, err := exec.LookPath("pg_isready"); err != nil {
		t.Skip("pg_isready not in PATH")
	}

	c := newCluster(t, "scripts_pg_isready")
	if err := c.Init(); err != nil {
		t.Fatal(err)
	}

	// server not yet started — pg_isready should fail
	res, err := c.RunClientTool("pg_isready", "--timeout=5")
	if err != nil {
		t.Fatal(err)
	}
	if res.ExitCode == 0 {
		t.Fatal("pg_isready should fail before server starts, got exit 0")
	}

	if err := c.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = c.Stop(cluster.ShutdownImmediate) }()

	// server running — pg_isready should succeed
	res, err = c.RunClientTool("pg_isready", "--timeout=10")
	if err != nil {
		t.Fatal(err)
	}
	if res.ExitCode != 0 {
		t.Fatalf("pg_isready should succeed when server is running: exit=%d stderr=%q",
			res.ExitCode, res.Stderr)
	}
}

// TestPort_Scripts010Clusterdb ports postgres/src/bin/scripts/t/010_clusterdb.pl.
//
// Upstream tests:
//   - SQL CLUSTER; runs
//   - fails with nonexistent table
//   - clusters a specific table using its index
//   - clusterdb with connection string
//
// Skip: goopg parser does not support the CLUSTER statement. Implement
// CLUSTER in parser+executor (at minimum a no-op stub) and remove this skip.
func TestPort_Scripts010Clusterdb(t *testing.T) {
	t.Skip("CLUSTER statement not supported in goopg parser/executor; " +
		"add parser+executor stub for CLUSTER before enabling")

	if _, err := exec.LookPath("clusterdb"); err != nil {
		t.Skip("clusterdb not in PATH")
	}

	c := newCluster(t, "scripts_clusterdb010")
	mustInitStart(t, c)
	defer func() { _ = c.Stop(cluster.ShutdownImmediate) }()

	// CLUSTER with no table — clusters all tables that have a previously
	// defined cluster index.
	res, err := c.RunClientTool("clusterdb")
	if err != nil {
		t.Fatal(err)
	}
	if res.ExitCode != 0 {
		t.Fatalf("clusterdb (all tables) failed: exit=%d stderr=%q", res.ExitCode, res.Stderr)
	}

	// Nonexistent table should fail.
	res, err = c.RunClientTool("clusterdb", "--table=nonexistent")
	if err != nil {
		t.Fatal(err)
	}
	if res.ExitCode == 0 {
		t.Fatal("clusterdb --table=nonexistent should fail")
	}

	// Create a clustered index then re-cluster.
	_, _ = c.PSQL("-c",
		"CREATE TABLE test1 (a int); CREATE INDEX test1x ON test1 (a); CLUSTER test1 USING test1x")
	res, err = c.RunClientTool("clusterdb", "--table=test1")
	if err != nil {
		t.Fatal(err)
	}
	if res.ExitCode != 0 {
		t.Fatalf("clusterdb --table=test1 failed: exit=%d stderr=%q", res.ExitCode, res.Stderr)
	}
}

// TestPort_Scripts011ClusterdbAll ports postgres/src/bin/scripts/t/011_clusterdb_all.pl.
//
// Upstream tests:
//   - cluster --all
//   - invalid DB handling
//
// Skip: requires CLUSTER SQL support (same as 010) and multi-database awareness.
func TestPort_Scripts011ClusterdbAll(t *testing.T) {
	t.Skip("CLUSTER statement not supported in goopg parser/executor; " +
		"add parser+executor stub for CLUSTER before enabling")

	if _, err := exec.LookPath("clusterdb"); err != nil {
		t.Skip("clusterdb not in PATH")
	}

	c := newCluster(t, "scripts_clusterdb011")
	mustInitStart(t, c)
	defer func() { _ = c.Stop(cluster.ShutdownImmediate) }()

	res, err := c.RunClientTool("clusterdb", "--all")
	if err != nil {
		t.Fatal(err)
	}
	if res.ExitCode != 0 {
		t.Fatalf("clusterdb --all failed: exit=%d stderr=%q", res.ExitCode, res.Stderr)
	}
}

// TestPort_Scripts020Createdb ports postgres/src/bin/scripts/t/020_createdb.pl.
//
// Upstream tests (representative subset):
//   - SQL CREATE DATABASE foobar1 runs
//   - create with encoding / locale / template options
//   - ICU locale provider (conditional on build config)
//   - WAL strategies (FILE_COPY / WAL_LOG)
//   - shared dependencies checks
//
// Skip: goopg parser does not support CREATE DATABASE. Add
// CREATE DATABASE to the parser and a stub executor entry before enabling.
func TestPort_Scripts020Createdb(t *testing.T) {
	t.Skip("CREATE DATABASE not supported in goopg parser/executor; " +
		"add parser+executor stub for CREATE DATABASE before enabling")

	if _, err := exec.LookPath("createdb"); err != nil {
		t.Skip("createdb not in PATH")
	}

	c := newCluster(t, "scripts_createdb020")
	mustInitStart(t, c)
	defer func() { _ = c.Stop(cluster.ShutdownImmediate) }()

	// Basic CREATE DATABASE.
	res, err := c.RunClientTool("createdb", "foobar1")
	if err != nil {
		t.Fatal(err)
	}
	if res.ExitCode != 0 {
		t.Fatalf("createdb foobar1 failed: exit=%d stderr=%q", res.ExitCode, res.Stderr)
	}

	// Create with LATIN1 encoding and C locale.
	res, err = c.RunClientTool("createdb",
		"--locale=C", "--encoding=LATIN1", "--template=template0", "foobar2")
	if err != nil {
		t.Fatal(err)
	}
	if res.ExitCode != 0 {
		t.Fatalf("createdb with encoding failed: exit=%d stderr=%q", res.ExitCode, res.Stderr)
	}
}

// TestPort_Scripts040Createuser ports postgres/src/bin/scripts/t/040_createuser.pl.
//
// Upstream tests (representative subset):
//   - CREATE ROLE with NOSUPERUSER, LOGIN, NOREPLICATION, etc.
//   - --no-login role
//   - CREATEROLE option
//   - superuser
//   - ADMIN / MEMBER options
//   - VALID UNTIL
//   - BYPASSRLS
//
// Skip: goopg parser does not support CREATE ROLE/USER. Add CREATE ROLE to
// the parser and a stub executor entry before enabling. Auth integration
// (WritePGAuth) may be used for actual user creation.
func TestPort_Scripts040Createuser(t *testing.T) {
	if _, err := exec.LookPath("createuser"); err != nil {
		t.Skip("createuser not in PATH")
	}

	c := newCluster(t, "scripts_createuser040")
	mustInitStart(t, c)
	defer func() { _ = c.Stop(cluster.ShutdownImmediate) }()

	// Basic CREATE ROLE with login.
	res, err := c.RunClientTool("createuser", "regress_user1")
	if err != nil {
		t.Fatal(err)
	}
	if res.ExitCode != 0 {
		t.Fatalf("createuser regress_user1 failed: exit=%d stderr=%q", res.ExitCode, res.Stderr)
	}

	// Non-login role.
	res, err = c.RunClientTool("createuser", "--no-login", "regress_role1")
	if err != nil {
		t.Fatal(err)
	}
	if res.ExitCode != 0 {
		t.Fatalf("createuser --no-login failed: exit=%d stderr=%q", res.ExitCode, res.Stderr)
	}

	// Superuser role.
	res, err = c.RunClientTool("createuser", "--superuser", "regress_user3")
	if err != nil {
		t.Fatal(err)
	}
	if res.ExitCode != 0 {
		t.Fatalf("createuser --superuser failed: exit=%d stderr=%q", res.ExitCode, res.Stderr)
	}
}

// TestPort_Scripts050Dropdb ports postgres/src/bin/scripts/t/050_dropdb.pl.
//
// Upstream tests:
//   - SQL DROP DATABASE runs
//   - DROP DATABASE ... WITH (FORCE) runs
//   - fails with nonexistent database
//   - invalid database (datconnlimit=-2) can be dropped
//
// Skip: goopg parser does not support DROP DATABASE. Also requires CREATE
// DATABASE to pre-create the target. Add both to parser+executor before enabling.
func TestPort_Scripts050Dropdb(t *testing.T) {
	t.Skip("DROP DATABASE not supported in goopg parser/executor; " +
		"add CREATE DATABASE + DROP DATABASE parser+executor stubs before enabling")

	if _, err := exec.LookPath("dropdb"); err != nil {
		t.Skip("dropdb not in PATH")
	}

	c := newCluster(t, "scripts_dropdb050")
	mustInitStart(t, c)
	defer func() { _ = c.Stop(cluster.ShutdownImmediate) }()

	// Pre-create then drop.
	if _, err := c.PSQL("-c", "CREATE DATABASE foobar1"); err != nil {
		t.Fatal(err)
	}
	res, err := c.RunClientTool("dropdb", "foobar1")
	if err != nil {
		t.Fatal(err)
	}
	if res.ExitCode != 0 {
		t.Fatalf("dropdb foobar1 failed: exit=%d stderr=%q", res.ExitCode, res.Stderr)
	}

	// Nonexistent database should fail.
	res, err = c.RunClientTool("dropdb", "nonexistent")
	if err != nil {
		t.Fatal(err)
	}
	if res.ExitCode == 0 {
		t.Fatal("dropdb nonexistent should fail, got exit 0")
	}
}

// TestPort_Scripts070Dropuser ports postgres/src/bin/scripts/t/070_dropuser.pl.
//
// Upstream tests:
//   - SQL DROP ROLE runs
//   - fails with nonexistent role
//
// Skip: goopg parser does not support DROP ROLE/USER. Also requires CREATE ROLE
// to pre-create the target. Add both to parser+executor before enabling.
func TestPort_Scripts070Dropuser(t *testing.T) {
	if _, err := exec.LookPath("dropuser"); err != nil {
		t.Skip("dropuser not in PATH")
	}

	c := newCluster(t, "scripts_dropuser070")
	mustInitStart(t, c)
	defer func() { _ = c.Stop(cluster.ShutdownImmediate) }()

	// Pre-create then drop.
	if _, err := c.PSQL("-c", "CREATE ROLE regress_foobar1"); err != nil {
		t.Fatal(err)
	}
	res, err := c.RunClientTool("dropuser", "regress_foobar1")
	if err != nil {
		t.Fatal(err)
	}
	if res.ExitCode != 0 {
		t.Fatalf("dropuser regress_foobar1 failed: exit=%d stderr=%q", res.ExitCode, res.Stderr)
	}

	// Nonexistent role should fail.
	res, err = c.RunClientTool("dropuser", "regress_nonexistent")
	if err != nil {
		t.Fatal(err)
	}
	if res.ExitCode == 0 {
		t.Fatal("dropuser nonexistent should fail, got exit 0")
	}
}

// TestPort_Scripts090Reindexdb ports postgres/src/bin/scripts/t/090_reindexdb.pl.
//
// Upstream tests (representative):
//   - REINDEX DATABASE postgres
//   - REINDEX with TABLESPACE
//   - REINDEX CONCURRENTLY
//   - relfilenode tracking (verifies actual work was done)
//
// Skip: goopg parser does not support the REINDEX statement. Add REINDEX to
// parser+executor (at minimum a no-op stub for DATABASE / TABLE forms) before
// enabling. Full relfilenode tracking requires storage-layer support.
func TestPort_Scripts090Reindexdb(t *testing.T) {
	if _, err := exec.LookPath("reindexdb"); err != nil {
		t.Skip("reindexdb not in PATH")
	}

	c := newCluster(t, "scripts_reindexdb090")
	mustInitStart(t, c)
	defer func() { _ = c.Stop(cluster.ShutdownImmediate) }()

	res, err := c.RunClientTool("reindexdb", "postgres")
	if err != nil {
		t.Fatal(err)
	}
	if res.ExitCode != 0 {
		t.Fatalf("reindexdb postgres failed: exit=%d stderr=%q", res.ExitCode, res.Stderr)
	}
}

// TestPort_Scripts091ReindexdbAll ports postgres/src/bin/scripts/t/091_reindexdb_all.pl.
//
// Upstream tests:
//   - reindexdb --all (across all databases)
//
// Skip: requires REINDEX SQL support (same as 090) plus multi-database
// awareness for --all.
func TestPort_Scripts091ReindexdbAll(t *testing.T) {
	if _, err := exec.LookPath("reindexdb"); err != nil {
		t.Skip("reindexdb not in PATH")
	}

	c := newCluster(t, "scripts_reindexdb091")
	mustInitStart(t, c)
	defer func() { _ = c.Stop(cluster.ShutdownImmediate) }()

	res, err := c.RunClientTool("reindexdb", "--all")
	if err != nil {
		t.Fatal(err)
	}
	if res.ExitCode != 0 {
		t.Fatalf("reindexdb --all failed: exit=%d stderr=%q", res.ExitCode, res.Stderr)
	}
}

// TestPort_Scripts100Vacuumdb ports postgres/src/bin/scripts/t/100_vacuumdb.pl.
//
// Upstream tests (selected):
//   - VACUUM; runs
//   - vacuumdb -f (FULL), -F (FREEZE)
//   - vacuumdb -zj2 (ANALYZE + PARALLEL), -Z (ANALYZE-only)
//   - vacuumdb --disable-page-skipping, --skip-locked, --no-index-cleanup
//   - vacuumdb --parallel
//   - vacuumdb with connection string
//   - column list, schema filter, min-xid-age, min-mxid-age
//   - --missing-stats-only
//
// Skip: vacuumdb sends VACUUM with the parenthesized option syntax
// (e.g., "VACUUM (SKIP_DATABASE_STATS, FULL) tablename;") because goopg
// reports server_version >= 160000 (currently "18.3"). goopg's VACUUM parser
// only accepts the legacy keyword form (VACUUM [VERBOSE] [ANALYZE]).
//
// To enable: extend parseVacuum() in internal/parser/parser.go to accept
// the parenthesized option list (VACUUM (opt [, opt ...]) [tablename ...])
// and extend VacuumStmt accordingly. Also requires pg_catalog.pg_namespace
// and additional pg_class columns for the table-discovery catalog query
// vacuumdb issues when no --table is specified.
func TestPort_Scripts100Vacuumdb(t *testing.T) {
	if _, err := exec.LookPath("vacuumdb"); err != nil {
		t.Skip("vacuumdb not in PATH")
	}

	c := newCluster(t, "scripts_vacuumdb100")
	mustInitStart(t, c)
	defer func() { _ = c.Stop(cluster.ShutdownImmediate) }()

	// Basic VACUUM.
	res, err := c.RunClientTool("vacuumdb", "postgres")
	if err != nil {
		t.Fatal(err)
	}
	if res.ExitCode != 0 {
		t.Fatalf("vacuumdb postgres failed: exit=%d stderr=%q", res.ExitCode, res.Stderr)
	}

	// VACUUM FULL.
	res, err = c.RunClientTool("vacuumdb", "-f", "postgres")
	if err != nil {
		t.Fatal(err)
	}
	if res.ExitCode != 0 {
		t.Fatalf("vacuumdb -f (FULL) failed: exit=%d stderr=%q", res.ExitCode, res.Stderr)
	}

	// VACUUM FREEZE.
	res, err = c.RunClientTool("vacuumdb", "-F", "postgres")
	if err != nil {
		t.Fatal(err)
	}
	if res.ExitCode != 0 {
		t.Fatalf("vacuumdb -F (FREEZE) failed: exit=%d stderr=%q", res.ExitCode, res.Stderr)
	}

	// ANALYZE only.
	res, err = c.RunClientTool("vacuumdb", "-Z", "postgres")
	if err != nil {
		t.Fatal(err)
	}
	if res.ExitCode != 0 {
		t.Fatalf("vacuumdb -Z (ANALYZE) failed: exit=%d stderr=%q", res.ExitCode, res.Stderr)
	}

	// Incompatible option pair should fail.
	res, err = c.RunClientTool("vacuumdb", "--analyze-only", "--disable-page-skipping", "postgres")
	if err != nil {
		t.Fatal(err)
	}
	if res.ExitCode == 0 {
		t.Fatal("vacuumdb --analyze-only --disable-page-skipping should fail")
	}

	// Negative parallel degree should fail.
	res, err = c.RunClientTool("vacuumdb", "--parallel=-1", "postgres")
	if err != nil {
		t.Fatal(err)
	}
	if res.ExitCode == 0 {
		t.Fatal("vacuumdb --parallel=-1 should fail")
	}
}

// TestPort_Scripts101VacuumdbAll ports postgres/src/bin/scripts/t/101_vacuumdb_all.pl.
//
// Upstream tests:
//   - vacuumdb --all (vacuums all databases)
//
// Skip: requires both VACUUM parenthesized syntax (see 100) and multi-database
// support (pg_database iteration) for --all.
func TestPort_Scripts101VacuumdbAll(t *testing.T) {
	if _, err := exec.LookPath("vacuumdb"); err != nil {
		t.Skip("vacuumdb not in PATH")
	}

	c := newCluster(t, "scripts_vacuumdb101")
	mustInitStart(t, c)
	defer func() { _ = c.Stop(cluster.ShutdownImmediate) }()

	res, err := c.RunClientTool("vacuumdb", "--all")
	if err != nil {
		t.Fatal(err)
	}
	if res.ExitCode != 0 {
		t.Fatalf("vacuumdb --all failed: exit=%d stderr=%q", res.ExitCode, res.Stderr)
	}
}

// TestPort_Scripts102VacuumdbStages ports postgres/src/bin/scripts/t/102_vacuumdb_stages.pl.
//
// Upstream tests:
//   - vacuumdb --analyze-in-stages (runs ANALYZE in three stages with
//     progressively increasing statistics targets, then a final normal ANALYZE)
//   - uses SET default_statistics_target = N between stages
//
// Skip: same VACUUM parenthesized syntax blocker as 100. The --analyze-in-stages
// path also depends on pg_catalog.pg_class query support. Enable after 100.
func TestPort_Scripts102VacuumdbStages(t *testing.T) {
	if _, err := exec.LookPath("vacuumdb"); err != nil {
		t.Skip("vacuumdb not in PATH")
	}

	c := newCluster(t, "scripts_vacuumdb102")
	mustInitStart(t, c)
	defer func() { _ = c.Stop(cluster.ShutdownImmediate) }()

	res, err := c.RunClientTool("vacuumdb", "--analyze-in-stages", "postgres")
	if err != nil {
		t.Fatal(err)
	}
	if res.ExitCode != 0 {
		t.Fatalf("vacuumdb --analyze-in-stages failed: exit=%d stderr=%q", res.ExitCode, res.Stderr)
	}
}

// TestPort_Scripts200Connstr ports postgres/src/bin/scripts/t/200_connstr.pl.
//
// Upstream tests:
//   - createdb with a LATIN1-encoded database name passed via connection string
//   - verifies that client tool connection-string handling works across encodings
//
// Skip: requires CREATE DATABASE (see 020) and LATIN1 server encoding support.
// goopg currently supports UTF8 only. Enable after multi-encoding and CREATE
// DATABASE land.
func TestPort_Scripts200Connstr(t *testing.T) {
	t.Skip("200_connstr requires CREATE DATABASE + LATIN1 server encoding; " +
		"enable after multi-encoding support (goopg currently UTF8-only) and " +
		"CREATE DATABASE parser+executor stub land")

	if _, err := exec.LookPath("createdb"); err != nil {
		t.Skip("createdb not in PATH")
	}

	c := newCluster(t, "scripts_connstr200")
	mustInitStart(t, c)
	defer func() { _ = c.Stop(cluster.ShutdownImmediate) }()

	// Create database with LATIN1 encoding (requires template0 as template).
	res, err := c.RunClientTool("createdb",
		"--encoding=LATIN1", "--locale=C", "--template=template0", "latin1db")
	if err != nil {
		t.Fatal(err)
	}
	if res.ExitCode != 0 {
		t.Fatalf("createdb latin1db failed: exit=%d stderr=%q", res.ExitCode, res.Stderr)
	}

	// Connect using the connection string containing the LATIN1 DB name.
	res, err = c.PSQL("dbname=latin1db", "-c", "SELECT 1")
	if err != nil {
		t.Fatal(err)
	}
	if res.ExitCode != 0 {
		t.Fatalf("connect to latin1db failed: exit=%d stderr=%q", res.ExitCode, res.Stderr)
	}

}
