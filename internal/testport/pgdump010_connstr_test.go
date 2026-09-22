package testport

import (
	"fmt"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/goopg/goopg/internal/testutil/cluster"
	"github.com/goopg/goopg/internal/testutil/util"
)

// TestPort_PgDump010ConnstrExoticNames ports the portable tier of
// postgres/src/bin/pg_dump/t/010_dump_connstr.pl (M0122-0015).
//
// Upstream's subject is NOT dump content — it is whether a database or role
// name that needs quoting survives every hop of the connection-string
// machinery. pg_dumpall builds a connstr per database and hands it to child
// psql/pg_dump processes, so a name containing a quote, a backslash or an `=`
// has to be escaped by the caller (PostgreSQL::Test::Cluster::connstr does
// `s#\\#\\\\#g` then `s#'#\\'#g`), parsed back by libpq, and then re-quoted as
// an SQL identifier on output. A bug anywhere in that chain either fails to
// connect or emits a dump that cannot be restored.
//
// WHAT IS PORTED HERE. Upstream sweeps the whole LATIN1 byte range across four
// database/role pairs. goopg's clusters are UTF-8 (no `--encoding=LATIN1`
// initdb arm), so the byte sweep is not portable as written; what IS portable —
// and is where the actual escaping logic lives — is a name carrying the ASCII
// metacharacters that break connstr parsing and identifier quoting:
// `'`, `\`, `=` and a space. This test uses one such database and one such
// role, and asserts upstream's two connection-string properties:
//
//	command_ok([pg_dumpall --roles-only --dbname <connstr> --username <weird>])
//	command_ok([pg_dumpall --dbname 'dbname=template1'])  # connstr, not a name
//
// Measured against goopg 2026-09-22: both pass, and the emitted role is
// correctly re-quoted as `CREATE ROLE "regress_a'b\c=d e";` — identifier
// quoting does not escape the backslash, matching PostgreSQL.
//
// WHAT IS NOT PORTED, and why it is not a formality (both ledgered):
//
//   - Upstream's parallel arms (`pg_dump --format=directory --jobs=2`, then
//     `pg_restore --jobs=2`) cannot run: goopg seeds `pg_export_snapshot` in
//     pg_proc (OID 3809) but has no executor dispatch arm for it, so pg_dump
//     fails at `SELECT pg_catalog.pg_export_snapshot()` before writing
//     anything. Parallel dump is unreachable, not merely slow.
//   - Upstream dumps a table from the exotically-named database. goopg cannot:
//     `COPY` resolves relations against the DEFAULT database regardless of the
//     connected one, so `COPY t1 TO stdout` raises `relation "t1" does not
//     exist` in any `CREATE DATABASE`-created database while `SELECT` on the
//     same relation in the same session succeeds. That makes `pg_dump` of any
//     user-created database emit schema but no data. Verified not to be about
//     the exotic name: a plainly-named `plaindb` reproduces it identically.
//
// The second is the discovery this port exists to record. It is a genuine
// pg_dump-parity defect that no existing test covers, because every other
// pg_dump port in this package dumps the `postgres` database.
func TestPort_PgDump010ConnstrExoticNames(t *testing.T) {
	bin := clientToolBin(t, "pg_dumpall")
	if bin == "" {
		t.Skip("pg_dumpall not in PATH or postgres/local_install/bin")
	}
	c := newCluster(t, "pgdump010connstr")
	mustInitStart(t, c)
	defer func() { _ = c.Stop(cluster.ShutdownImmediate) }()

	// The metacharacters that matter to connstr parsing and to identifier
	// quoting, in one name each. Upstream's equivalents are assembled by
	// generate_ascii_string() over the same ASCII ranges.
	const dbName = `regress=a'b\c d`
	const roleName = `regress_a'b\c=d e`

	if err := runSQLSimple(t, c, `CREATE DATABASE "`+dbName+`"`); err != nil {
		t.Fatalf("create database with quoting metacharacters: %v", err)
	}
	// A superuser, because upstream connects AS this role to run pg_dumpall.
	if err := runSQLSimple(t, c, `CREATE ROLE "`+roleName+`" SUPERUSER LOGIN`); err != nil {
		t.Fatalf("create role with quoting metacharacters: %v", err)
	}

	// The names must come back verbatim, or the connstr built below would
	// address a different object and the rest of the test would be vacuous.
	if got := queryScalar(t, c, `SELECT count(*) FROM pg_database WHERE datname = '`+strings.ReplaceAll(dbName, `'`, `''`)+`'`); got != "1" {
		t.Fatalf("pg_database count for the quoted name = %q, want 1 "+
			"(CREATE DATABASE did not store the name verbatim)", got)
	}

	host, port, err := net.SplitHostPort(c.ListenAddr())
	if err != nil {
		t.Fatalf("split listen addr %q: %v", c.ListenAddr(), err)
	}
	// Exactly PostgreSQL::Test::Cluster::connstr's escaping: backslashes
	// first, then single quotes. Doing it in the other order would
	// double-escape the backslash it just introduced.
	esc := strings.ReplaceAll(dbName, `\`, `\\`)
	esc = strings.ReplaceAll(esc, `'`, `\'`)
	connstr := fmt.Sprintf("host=%s port=%s dbname='%s'", host, port, esc)

	// Upstream: 'pg_dumpall with long ASCII name N'. --roles-only because it
	// produces a short dump (and because the data half is blocked, see above).
	res, err := util.RunCommand(util.CommandSpec{
		Name:    bin,
		Args:    []string{"--no-sync", "--roles-only", "--dbname", connstr, "--username", roleName},
		Env:     amcheckEnv(t, c),
		Timeout: 60 * time.Second,
	})
	if err != nil {
		t.Fatalf("run pg_dumpall with exotic connstr: %v", err)
	}
	if res.ExitCode != 0 {
		t.Fatalf("pg_dumpall --dbname <exotic connstr> --username <exotic> exited %d, want 0\nstdout=%s\nstderr=%s",
			res.ExitCode, res.Stdout, res.Stderr)
	}
	// The role must be re-quoted as an SQL identifier on the way out. PG does
	// NOT escape the backslash inside a quoted identifier, so the emitted text
	// carries it literally; asserting the exact string is what would catch a
	// quoter that "helpfully" doubled it and produced an unrestorable dump.
	wantRole := `CREATE ROLE "` + roleName + `";`
	if !strings.Contains(res.Stdout, wantRole) {
		t.Errorf("pg_dumpall output does not carry the correctly quoted role\nwant substring: %s\nstdout=%s",
			wantRole, res.Stdout)
	}

	// Upstream: 'pg_dumpall --dbname accepts connection string'. The point is
	// that --dbname takes a CONNECTION STRING, not only a database name;
	// upstream uses template1, which goopg also has.
	res2, err := util.RunCommand(util.CommandSpec{
		Name:    bin,
		Args:    []string{"--no-sync", "--roles-only", "--dbname", "dbname=postgres"},
		Env:     amcheckEnv(t, c),
		Timeout: 60 * time.Second,
	})
	if err != nil {
		t.Fatalf("run pg_dumpall --dbname=connstr: %v", err)
	}
	if res2.ExitCode != 0 {
		t.Fatalf("pg_dumpall --dbname 'dbname=postgres' exited %d, want 0 "+
			"(--dbname must accept a connection string, not only a name)\nstderr=%s",
			res2.ExitCode, res2.Stderr)
	}
}
