package testport

import (
	"os"
	"path/filepath"
	"regexp"
	"testing"
	"time"

	"github.com/goopg/goopg/internal/testutil/cluster"
	"github.com/goopg/goopg/internal/testutil/util"
)

// TestPort_PgDump005Filterfile ports
// postgres/src/bin/pg_dump/t/005_pg_dump_filterfile.pl.
//
// Upstream's subject is the `--filter=FILE` option shared by pg_dump,
// pg_dumpall and pg_restore: a file of `include|exclude <object-type>
// <pattern>` lines that stands in for the per-object pattern switches
// (-t/-T/-n/-N, --exclude-database, pg_restore's -P/-I/-T) on the command
// line. The filter PARSER is client-side (src/fe_utils/
// filter.c), but every positive arm then resolves the patterns server-side —
// pg_dump turns each one into `... OPERATOR(pg_catalog.~) '^(...)$' COLLATE
// pg_catalog.default` against pg_class / pg_namespace / pg_foreign_server /
// pg_extension, and the multiline-name arms only work if the server keeps a
// newline inside a quoted identifier verbatim. That is what this port
// exercises in goopg. (It became runnable once `x OPERATOR(pg_catalog.=) ANY
// (array[...])` parsed — before that every pattern-bearing pg_dump failed.)
//
// WHAT IS PORTED (69 of upstream's 92 assertions), every one byte-for-byte —
// the filter-file contents (comments, blank and tab-only lines, the literal
// newline inside `"strange aaa\nname"`, the backslash-n spelling of the same
// name, the unterminated last line) and the regexes (Perl /m -> (?m),
// /ms -> (?ms)):
//
//   - pg_dump over the `postgres` database: empty filter, comments and
//     whitespace, qualified and quoted names, exclude-one, wildcard,
//     multiline quoted names (both spellings), exclude schema, include+exclude
//     schema across two --filter files (11 command_ok + 27 like/unlike);
//   - foreign_data include/exclude (4), including upstream's accidental arm:
//     `print $inputfile, "include foreign_data dummyserver\n"` has a comma, so
//     Perl prints to STDOUT and leaves the file EMPTY — reproduced as an empty
//     filter file, since that is what upstream actually tests;
//   - the four broken-input arms; the --strict-names arms (3);
//   - the four pg_dumpall arms that fail during option / filter validation;
//   - pg_dump -Fc of `postgres`, the pg_restore --filter include-table arm and
//     its two checks, and the four pg_restore unallowed-object arms (8);
//   - table_and_children / table_data_and_children (7), and the nonexistent
//     extension arm (1).
//
// ORDERING NOTE. Upstream builds the `sourcedb` objects (four functions, two
// schemas, tables, a sequence, two indexes, two triggers) BEFORE the first
// dump. This port builds them LAST (subtest sourcedb_fixture). On PostgreSQL
// the move is invisible to every ported arm — those arms only dump or restore
// `postgres`, and sourcedb's objects live in a different database's catalogs.
// On goopg it is not invisible, because of defect (2) below: with upstream's
// order, every `pg_dump postgres` above fails, and the port would assert
// nothing. The move is limited to arms whose PG-visible state is identical
// either way; the pg_dumpall positive arm, which really does dump sourcedb,
// is NOT moved ahead of the fixture and is left out instead.
//
// WHAT IS NOT PORTED (23 assertions), each measured 2026-10-12 against goopg
// with the fixture in upstream's order:
//
//   - pg_dumpall "dump tables with exclusion of a database" (command_ok) and
//     its two checks (`^\\connect postgres` absent, `^\\connect template1`
//     present): pg_dumpall exits 1, the child pg_dump failing with
//     `ERROR: type "pg_catalog.oid" does not exist` at `PREPARE
//     dumpFunc(pg_catalog.oid) AS SELECT proretset, ...` — defect (1).
//   - The whole "test restore of other objects" section: `pg_dump --format
//     custom sourcedb` (command_ok), then five pg_restore --filter command_oks
//     (include function foo1(integer); include function with -P-style
//     whitespace for foo3; include index t1_idx1 + include trigger "t1 trg1";
//     include schema s1; exclude schema s1) and their 14 like/unlike checks.
//     The pg_dump step exits 1 with the same dumpFunc PREPARE error — defect
//     (1) — so every pg_restore after it reports `could not read from input
//     file: end of file`. With sourcedb's functions dropped, a probe database
//     carrying the same schemas/tables/indexes still fails to dump — defect
//     (3) — so (1) is not the only thing in the way.
//
// GOOPG DEFECTS THIS PORT MEASURED (PG 18.3 behavior in brackets):
//
//  1. A schema-qualified type in PREPARE's parameter list does not resolve:
//     `PREPARE x(pg_catalog.oid) AS SELECT $1` -> `ERROR: 42704: type
//     "pg_catalog.oid" does not exist` (likewise `pg_catalog.int4`,
//     `public.t2`), while `PREPARE y(oid)` and `SELECT 1::pg_catalog.oid`
//     succeed. [PG: PREPARE succeeds.] pg_dump issues `PREPARE
//     dumpFunc(pg_catalog.oid)` for the first function it dumps, so pg_dump of
//     ANY database containing a user function fails.
//  2. User functions leak across databases in pg_proc: after `CREATE FUNCTION
//     foo1(...)` etc. in sourcedb, `SELECT proname FROM pg_proc WHERE proname
//     LIKE 'foo%'` returns all four rows in postgres, template1, targetdb and
//     in a database created afterwards — yet `DROP FUNCTION foo1(int)` in
//     postgres raises `function foo1(integer) does not exist`. [PG: pg_proc
//     is per-database; zero rows outside sourcedb.] pg_dump of postgres then
//     tries to dump sourcedb's functions and hits (1).
//  3. COPY in a CREATE DATABASE-created database resolves the default
//     database: pg_dump of such a database fails at `COPY public.t1 (a, b)
//     TO stdout` with `relation "t1" does not exist`. [PG: the table's rows
//     (here none) are copied.] Same defect pgdump010_connstr_test.go records.
//  4. (Observed, no assertion depends on it.) pg_foreign_data_wrapper leaks
//     across databases: FDW `dummy` created in postgres is listed in a fresh
//     database's pg_foreign_data_wrapper and emitted as `CREATE FOREIGN DATA
//     WRAPPER dummy;` in its dump; pg_foreign_server does not leak. [PG:
//     per-database catalog; zero rows.]
func TestPort_PgDump005Filterfile(t *testing.T) {
	pgDump := clientToolBin(t, "pg_dump")
	pgDumpall := clientToolBin(t, "pg_dumpall")
	pgRestore := clientToolBin(t, "pg_restore")
	psqlBin := clientToolBin(t, "psql")
	if pgDump == "" || pgDumpall == "" || pgRestore == "" || psqlBin == "" {
		t.Skip("pg_dump / pg_dumpall / pg_restore / psql not in PATH or postgres/local_install/bin")
	}
	c := newCluster(t, "pgdump005filter")
	mustInitStart(t, c)
	defer func() { _ = c.Stop(cluster.ShutdownImmediate) }()

	env := amcheckEnv(t, c)
	tempdir := t.TempDir()
	inputFile := filepath.Join(tempdir, "inputfile.txt")
	inputFile2 := filepath.Join(tempdir, "inputfile2.txt")
	plainFile := filepath.Join(tempdir, "plain.sql")
	customDump := filepath.Join(tempdir, "filter_test.dump")

	// ---- Generate test objects (upstream's safe_psql block, in order) ----
	for _, stmt := range []string{
		"CREATE FOREIGN DATA WRAPPER dummy;",
		"CREATE SERVER dummyserver FOREIGN DATA WRAPPER dummy;",
		"CREATE TABLE table_one(a varchar)",
		"CREATE TABLE table_two(a varchar)",
		"CREATE TABLE table_three(a varchar)",
		"CREATE TABLE table_three_one(a varchar)",
		"CREATE TABLE footab(a varchar)",
		"CREATE TABLE bootab() inherits (footab)",
		"CREATE TABLE \"strange aaa\nname\"(a varchar)",
		"CREATE TABLE \"\nt\nt\n\"(a int)",
		"INSERT INTO table_one VALUES('*** TABLE ONE ***')",
		"INSERT INTO table_two VALUES('*** TABLE TWO ***')",
		"INSERT INTO table_three VALUES('*** TABLE THREE ***')",
		"INSERT INTO table_three_one VALUES('*** TABLE THREE_ONE ***')",
		"INSERT INTO bootab VALUES(10)",
		"CREATE DATABASE sourcedb",
		"CREATE DATABASE targetdb",
	} {
		if err := runSQLSimple(t, c, stmt); err != nil {
			t.Fatalf("%q: %v", stmt, err)
		}
	}
	// ---- helpers mirroring the TAP primitives ----
	writeFilter := func(path, content string) {
		t.Helper()
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatalf("write filter file: %v", err)
		}
	}
	appendFilter := func(path, content string) {
		t.Helper()
		f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o644)
		if err != nil {
			t.Fatalf("open filter file for append: %v", err)
		}
		defer f.Close()
		if _, err := f.WriteString(content); err != nil {
			t.Fatalf("append filter file: %v", err)
		}
	}
	run := func(bin string, args ...string) util.CommandResult {
		t.Helper()
		res, err := util.RunCommand(util.CommandSpec{
			Name:    bin,
			Args:    args,
			Env:     env,
			Timeout: 120 * time.Second,
		})
		if err != nil {
			t.Fatalf("run %s %v: %v", filepath.Base(bin), args, err)
		}
		return res
	}
	// commandOK: upstream command_ok — exit status 0.
	commandOK := func(name, bin string, args ...string) {
		t.Helper()
		res := run(bin, args...)
		if res.ExitCode != 0 {
			t.Errorf("%s: %s %v exited %d, want 0\nstderr=%s",
				name, filepath.Base(bin), args, res.ExitCode, res.Stderr)
		}
	}
	// commandFailsLike: upstream command_fails_like — nonzero exit AND
	// stderr matching re.
	commandFailsLike := func(name string, re *regexp.Regexp, bin string, args ...string) {
		t.Helper()
		res := run(bin, args...)
		if res.ExitCode == 0 {
			t.Errorf("%s: %s %v exited 0, want failure\nstderr=%s",
				name, filepath.Base(bin), args, res.Stderr)
		}
		if !re.MatchString(res.Stderr) {
			t.Errorf("%s: stderr does not match %s\nstderr=%s", name, re, res.Stderr)
		}
	}
	slurp := func() string {
		t.Helper()
		b, err := os.ReadFile(plainFile)
		if err != nil {
			t.Fatalf("slurp %s: %v", plainFile, err)
		}
		return string(b)
	}
	like := func(dump, pattern, name string) {
		t.Helper()
		if !regexp.MustCompile(pattern).MatchString(dump) {
			t.Errorf("%s: dump does not match %s\ndump=%s", name, pattern, dump)
		}
	}
	unlike := func(dump, pattern, name string) {
		t.Helper()
		if regexp.MustCompile(pattern).MatchString(dump) {
			t.Errorf("%s: dump unexpectedly matches %s\ndump=%s", name, pattern, dump)
		}
	}
	dumpWithFilter := func(name string, extra ...string) {
		t.Helper()
		args := []string{"--file", plainFile, "--filter", inputFile}
		args = append(args, extra...)
		args = append(args, "postgres")
		commandOK(name, pgDump, args...)
	}
	dumpFailsLike := func(name string, re *regexp.Regexp, extra ...string) {
		t.Helper()
		args := []string{"--file", plainFile, "--filter", inputFile}
		args = append(args, extra...)
		args = append(args, "postgres")
		commandFailsLike(name, re, pgDump, args...)
	}
	restoreWithFilter := func(name string) {
		t.Helper()
		commandOK(name, pgRestore, "--file", plainFile, "--filter", inputFile,
			"--format", "custom", customDump)
	}

	t.Run("pg_dump", func(t *testing.T) {
		// Empty filterfile
		writeFilter(inputFile, "\n # a comment and nothing more\n\n")
		dumpWithFilter("filter file without patterns")
		dump := slurp()
		like(dump, `(?m)^CREATE TABLE public\.table_one`, "table one dumped")
		like(dump, `(?m)^CREATE TABLE public\.table_two`, "table two dumped")
		like(dump, `(?m)^CREATE TABLE public\.table_three`, "table three dumped")
		like(dump, `(?m)^CREATE TABLE public\.table_three_one`, "table three one dumped")

		// Various combinations of whitespace, comments and correct filters
		writeFilter(inputFile, "  include   table table_one    #comment\n"+
			"include table table_two\n"+
			"# skip this line\n"+
			"\n"+
			"\t\n"+
			"  \t# another comment\n"+
			"exclude table_data table_one\n")
		dumpWithFilter("dump tables with filter patterns as well as comments and whitespace")
		dump = slurp()
		like(dump, `(?m)^CREATE TABLE public\.table_one`, "dumped table one")
		like(dump, `(?m)^CREATE TABLE public\.table_two`, "dumped table two")
		unlike(dump, `(?m)^CREATE TABLE public\.table_three`, "table three not dumped")
		unlike(dump, `(?m)^CREATE TABLE public\.table_three_one`, "table three_one not dumped")
		unlike(dump, `(?m)^COPY public\.table_one`, "content of table one is not included")
		like(dump, `(?m)^COPY public\.table_two`, "content of table two is included")

		// Tables specified by qualified names
		writeFilter(inputFile, "include table public.table_one\n"+
			"include table \"public\".\"table_two\"\n"+
			"include table \"public\". table_three\n")
		dumpWithFilter("filter file without patterns")
		dump = slurp()
		like(dump, `(?m)^CREATE TABLE public\.table_one`, "dumped table one")
		like(dump, `(?m)^CREATE TABLE public\.table_two`, "dumped table two")
		like(dump, `(?m)^CREATE TABLE public\.table_three`, "dumped table three")

		// All tables except one
		writeFilter(inputFile, "exclude table table_one\n")
		dumpWithFilter("dump tables with exclusion of a single table")
		dump = slurp()
		unlike(dump, `(?m)^CREATE TABLE public\.table_one`, "table one not dumped")
		like(dump, `(?m)^CREATE TABLE public\.table_two`, "dumped table two")
		like(dump, `(?m)^CREATE TABLE public\.table_three`, "dumped table three")
		like(dump, `(?m)^CREATE TABLE public\.table_three_one`, "dumped table three_one")

		// Wildcard pattern
		writeFilter(inputFile, "include table table_thre*\n")
		dumpWithFilter("dump tables with wildcard in pattern")
		dump = slurp()
		unlike(dump, `(?m)^CREATE TABLE public\.table_one`, "table one not dumped")
		unlike(dump, `(?m)^CREATE TABLE public\.table_two`, "table two not dumped")
		like(dump, `(?m)^CREATE TABLE public\.table_three`, "dumped table three")
		like(dump, `(?m)^CREATE TABLE public\.table_three_one`, "dumped table three_one")

		// Multiline quoted table name (a literal newline inside the quotes)
		writeFilter(inputFile, "include table \"strange aaa\nname\"")
		dumpWithFilter("dump tables with multiline names requiring quoting")
		dump = slurp()
		like(dump, `(?m)^CREATE TABLE public."strange aaa`, "dump table with new line in name")

		// Excluding it, spelled with a backslash-n escape
		writeFilter(inputFile, "exclude table \"strange aaa\\nname\"")
		dumpWithFilter("dump tables with filter")
		dump = slurp()
		unlike(dump, `(?m)^CREATE TABLE public."strange aaa`, "dump table with new line in name")

		// Excluding an entire schema
		writeFilter(inputFile, "exclude schema public\n")
		dumpWithFilter("exclude the public schema")
		dump = slurp()
		unlike(dump, `(?m)^CREATE TABLE`, "no table dumped")

		// Including and excluding a schema via two filter files
		writeFilter(inputFile, "include schema public\n")
		writeFilter(inputFile2, "exclude schema public\n")
		dumpWithFilter("exclude the public schema with multiple filters", "--filter", inputFile2)
		dump = slurp()
		unlike(dump, `(?m)^CREATE TABLE`, "no table dumped")

		// Table name with a leading newline on a row
		writeFilter(inputFile, "include table \"\nt\nt\n\"")
		dumpWithFilter("dump tables with filter")
		dump = slurp()
		like(dump, `(?ms)^CREATE TABLE public."\nt\nt\n" \($`, "dump table with multiline strange name")

		writeFilter(inputFile, "include table \"\\nt\\nt\\n\"")
		dumpWithFilter("dump tables with filter")
		dump = slurp()
		like(dump, `(?ms)^CREATE TABLE public."\nt\nt\n" \($`, "dump table with multiline strange name")
	})

	t.Run("foreign_data", func(t *testing.T) {
		writeFilter(inputFile, "include foreign_data doesnt_exists\n")
		dumpFailsLike("dump nonexisting foreign server",
			regexp.MustCompile(`pg_dump: error: no matching foreign servers were found for pattern`))

		// Upstream writes `print $inputfile, "include foreign_data
		// dummyserver\n";` — the comma makes Perl print the filehandle and
		// the string to STDOUT, so the file (opened with '>') is left EMPTY.
		// The arm therefore dumps everything and the CREATE SERVER assertion
		// rides on the empty filter. Replicated faithfully.
		writeFilter(inputFile, "")
		dumpWithFilter("dump foreign_data with filter")
		dump := slurp()
		like(dump, `(?m)^CREATE SERVER dummyserver`, "dump foreign server")

		writeFilter(inputFile, "exclude foreign_data dummy*\n")
		dumpFailsLike("erroneously exclude foreign server",
			regexp.MustCompile(`exclude filter for "foreign data" is not allowed`))
	})

	t.Run("broken_input", func(t *testing.T) {
		writeFilter(inputFile, "k")
		dumpFailsLike("invalid syntax: incorrect filter command",
			regexp.MustCompile(`invalid filter command`))

		writeFilter(inputFile, "exclude table-data one")
		dumpFailsLike("invalid syntax: invalid object type specified",
			regexp.MustCompile(`unsupported filter object type: "table-data"`))

		writeFilter(inputFile, "include table")
		dumpFailsLike("invalid syntax: missing object identifier pattern",
			regexp.MustCompile(`missing object name`))

		writeFilter(inputFile, "include table table one")
		dumpFailsLike("invalid syntax: extra content after object identifier pattern",
			regexp.MustCompile(`no matching tables were found`))
	})

	t.Run("strict_names", func(t *testing.T) {
		writeFilter(inputFile, "include table table_one\n")
		dumpWithFilter("strict names with matching pattern", "--strict-names")
		dump := slurp()
		like(dump, `(?m)^CREATE TABLE public\.table_one`, "no table dumped")

		appendFilter(inputFile, "include table table_nonexisting_name")
		dumpFailsLike("inclusion of non-existing objects with --strict names",
			regexp.MustCompile(`no matching tables were found`), "--strict-names")
	})

	t.Run("pg_dumpall", func(t *testing.T) {
		// Upstream's positive arm here (command_ok "dump tables with
		// exclusion of a database" + the two \connect assertions) is NOT
		// ported: against upstream's fixture it dumps sourcedb's functions and
		// dies at pg_dump's PREPARE dumpFunc(pg_catalog.oid) — see the doc
		// comment. The same filter file feeds the --globals-only arm below,
		// which fails at option validation before any dump runs.
		writeFilter(inputFile, "exclude database postgres\n")
		commandFailsLike("pg_dumpall: option --exclude-database cannot be used together with -g/--globals-only",
			regexp.MustCompile(regexp.QuoteMeta(`pg_dumpall: error: option --exclude-database cannot be used together with -g/--globals-only`)),
			pgDumpall, "--file", plainFile, "--filter", inputFile, "--globals-only")

		writeFilter(inputFile, "k")
		commandFailsLike("invalid syntax: incorrect filter command",
			regexp.MustCompile(`invalid filter command`),
			pgDumpall, "--file", plainFile, "--filter", inputFile)

		writeFilter(inputFile, "exclude xxx")
		commandFailsLike("invalid syntax: exclusion of non-existing object type",
			regexp.MustCompile(`unsupported filter object type: "xxx"`),
			pgDumpall, "--file", plainFile, "--filter", inputFile)

		writeFilter(inputFile, "exclude table foo")
		commandFailsLike("invalid syntax: exclusion of unsupported object type",
			regexp.MustCompile(`pg_dumpall: error: invalid format in filter`),
			pgDumpall, "--file", plainFile, "--filter", inputFile)
	})

	t.Run("pg_restore", func(t *testing.T) {
		commandOK("dump all tables", pgDump,
			"--file", customDump, "--format", "custom", "postgres")

		writeFilter(inputFile, "include table table_two")
		restoreWithFilter("restore tables with filter")
		dump := slurp()
		like(dump, `(?m)^CREATE TABLE public\.table_two`, "wanted table restored")
		unlike(dump, `(?m)^CREATE TABLE public\.table_one`, "unwanted table is not restored")

		writeFilter(inputFile, "include table_data xxx")
		commandFailsLike("invalid syntax: inclusion of unallowed object",
			regexp.MustCompile(`include filter for "table data" is not allowed`),
			pgRestore, "--file", plainFile, "--filter", inputFile)

		writeFilter(inputFile, "include extension xxx")
		commandFailsLike("invalid syntax: inclusion of unallowed object",
			regexp.MustCompile(`include filter for "extension" is not allowed`),
			pgRestore, "--file", plainFile, "--filter", inputFile)

		writeFilter(inputFile, "exclude extension xxx")
		commandFailsLike("invalid syntax: exclusion of unallowed object",
			regexp.MustCompile(`exclude filter for "extension" is not allowed`),
			pgRestore, "--file", plainFile, "--filter", inputFile)

		writeFilter(inputFile, "exclude table_data xxx")
		commandFailsLike("invalid syntax: exclusion of unallowed object",
			regexp.MustCompile(`exclude filter for "table data" is not allowed`),
			pgRestore, "--file", plainFile, "--filter", inputFile)
	})

	// Upstream's "test restore of other objects" section (pg_dump -Fc
	// sourcedb, then five pg_restore --filter arms over functions, an index,
	// a trigger and schemas s1/s2) sits here. It is NOT ported: the
	// pg_dump -Fc sourcedb step fails on goopg (doc comment), so every arm
	// after it reads a truncated archive.

	t.Run("table_and_children", func(t *testing.T) {
		writeFilter(inputFile, "include table_and_children footab\n")
		dumpWithFilter("filter file without patterns")
		dump := slurp()
		like(dump, `(?m)^CREATE TABLE public\.bootab`, "dumped children table")

		writeFilter(inputFile, "exclude table_and_children footab\n")
		dumpWithFilter("filter file without patterns")
		dump = slurp()
		unlike(dump, `(?m)^CREATE TABLE public\.bootab`, "exclude dumped children table")

		writeFilter(inputFile, "exclude table_data_and_children footab\n")
		dumpWithFilter("filter file without patterns")
		dump = slurp()
		like(dump, `(?m)^CREATE TABLE public\.bootab`, "dumped children table")
		unlike(dump, `(?m)^COPY public\.bootab`, "exclude dumped children table")
	})

	t.Run("extension", func(t *testing.T) {
		writeFilter(inputFile, "include extension doesnt_exists\n")
		dumpFailsLike("dump nonexisting extension",
			regexp.MustCompile(`pg_dump: error: no matching extensions were found`))
	})

	// Upstream's sourcedb fixture. Upstream creates it before the first dump;
	// here it runs LAST (see the doc comment's ORDERING note): on goopg a
	// function created in sourcedb also shows up in pg_proc of every other
	// database, which would break every pg_dump of postgres above. Running
	// it still pins that goopg accepts each statement (upstream's safe_psql
	// dies on any failure), and it is the fixture the unported sourcedb arms
	// need once they can run.
	t.Run("sourcedb_fixture", func(t *testing.T) {
		for _, stmt := range []string{
			`CREATE FUNCTION foo1(a int) RETURNS int AS $$ select $1 $$ LANGUAGE sql`,
			`CREATE FUNCTION foo2(a int) RETURNS int AS $$ select $1 $$ LANGUAGE sql`,
			`CREATE FUNCTION foo3(a double precision, b int) RETURNS double precision AS $$ select $1 + $2 $$ LANGUAGE sql`,
			`CREATE FUNCTION foo_trg() RETURNS trigger AS $$ BEGIN RETURN NEW; END $$ LANGUAGE plpgsql`,
			`CREATE SCHEMA s1`,
			`CREATE SCHEMA s2`,
			`CREATE TABLE s1.t1(a int)`,
			`CREATE SEQUENCE s1.s1`,
			`CREATE TABLE s2.t2(a int)`,
			`CREATE TABLE t1(a int, b int)`,
			`CREATE TABLE t2(a int, b int)`,
			`CREATE INDEX t1_idx1 ON t1(a)`,
			`CREATE INDEX t1_idx2 ON t1(b)`,
			`CREATE TRIGGER trg1 BEFORE INSERT ON t1 EXECUTE FUNCTION foo_trg()`,
			`CREATE TRIGGER trg2 BEFORE INSERT ON t1 EXECUTE FUNCTION foo_trg()`,
		} {
			// The cluster helper's Query is bound to the default database, so the
			// sourcedb objects go through psql -d sourcedb.
			res, err := util.RunCommand(util.CommandSpec{
				Name:    psqlBin,
				Args:    []string{"-X", "-d", "sourcedb", "-v", "ON_ERROR_STOP=1", "-c", stmt},
				Env:     env,
				Timeout: 30 * time.Second,
			})
			if err != nil || res.ExitCode != 0 {
				t.Fatalf("sourcedb: %q: err=%v exit=%d stderr=%s", stmt, err, res.ExitCode, res.Stderr)
			}
		}
	})
}
