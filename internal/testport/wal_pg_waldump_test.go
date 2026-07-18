package testport

// wal_pg_waldump_test.go — TestPort_WALPgWaldumpCompat
//
// Verifies that goopg's PG-compatible WAL format (PageHeaders=true,
// enabled by M0101-0001) can be parsed by the upstream pg_waldump binary.
// This is the oracle-test gate for M0101-0003.
//
// Test flow:
//  1. Start a goopg cluster (which now writes PG-compatible WAL).
//  2. Run a small workload: CREATE TABLE + INSERT 100 rows + CHECKPOINT.
//  3. Stop the cluster cleanly.
//  4. Enumerate pg_wal/ segments (native PG-format names, TLI=1 prefix).
//  5. Run pg_waldump --quiet directly on each segment (pg_waldump infers the
//     timeline and start LSN from the filename — no alias rewriting needed).
//  6. Assert no structural error (notably "incorrect prev-link") for any
//     segment — this is the regression guard for the xl_prev seeding fix in
//     internal/wal/writer.go (detectWritePos). End-of-WAL on the trailing
//     segment is expected and ignored.

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/goopg/goopg/internal/testutil/cluster"
	"github.com/goopg/goopg/internal/testutil/util"
)

// TestPort_WALPgWaldumpCompat verifies that goopg's WAL segments are
// readable by pg_waldump. M0101-0003.
func TestPort_WALPgWaldumpCompat(t *testing.T) {
	waldump := findPGWaldumpBin(t)

	c := newCluster(t, "wal_pg_waldump")
	mustInitStart(t, c)
	defer func() { _ = c.Stop(cluster.ShutdownImmediate) }()

	// Run a workload that generates WAL.
	addr := c.ListenAddr()
	host, port, _ := splitHostPort(addr)
	psqlBin := clientToolBin(t, "psql")
	if psqlBin == "" {
		t.Skip("psql not available")
	}

	root := repoRoot(t)
	libDir := filepath.Join(root, "postgres", "local_install", "lib")
	psqlEnv := []string{"PGPASSWORD=", "LD_LIBRARY_PATH=" + libDir}

	// B1: schema/function/sequence DDL now journals as real catalog heap
	// records (pg_namespace/pg_proc/pg_sequence INSERT/UPDATE/DELETE) —
	// include one of each so real pg_waldump structurally validates them.
	workload := `
CREATE TABLE wal_test (id serial primary key, val text);
CREATE SCHEMA waldump_s1;
CREATE SCHEMA waldump_s1x;
ALTER SCHEMA waldump_s1 RENAME TO waldump_s2;
CREATE FUNCTION waldump_add(a int, b int) RETURNS int LANGUAGE sql AS 'SELECT a + b';
CREATE SEQUENCE waldump_seq INCREMENT 3;
ALTER SEQUENCE waldump_seq MAXVALUE 500;
CREATE DOMAIN waldump_dom AS text CHECK (VALUE IN ('a', 'b'));
ALTER DOMAIN waldump_dom SET DEFAULT 'a';
DROP DOMAIN waldump_dom;
CREATE CAST (int4 AS bool) WITH INOUT;
DROP CAST (int4 AS bool);
CREATE AGGREGATE waldump_agg(int) (SFUNC = waldump_add, STYPE = int, INITCOND = '0');
ALTER AGGREGATE waldump_agg(int) RENAME TO waldump_agg2;
DROP AGGREGATE waldump_agg2(int);
CREATE OPERATOR <+> (LEFTARG = int, RIGHTARG = int, FUNCTION = waldump_add, COMMUTATOR = OPERATOR(<+>));
DROP OPERATOR <+> (int, int);
CREATE OPERATOR public.~=~ (FUNCTION = int4eq, LEFTARG = int4, RIGHTARG = int4);
CREATE OPERATOR FAMILY public.waldump_fam USING btree;
CREATE OPERATOR CLASS public.waldump_class FOR TYPE int4 USING btree FAMILY public.waldump_fam AS OPERATOR 1 ~=~ (int4, int4), FUNCTION 1 int4eq(int4, int4);
ALTER OPERATOR FAMILY public.waldump_fam USING btree ADD OPERATOR 3 ~=~ (int4, int4);
ALTER OPERATOR FAMILY public.waldump_fam USING btree DROP OPERATOR 3 (int4, int4);
DROP OPERATOR CLASS public.waldump_class USING btree;
CREATE TRANSFORM FOR int LANGUAGE sql (FROM SQL WITH FUNCTION prsd_lextype(internal), TO SQL WITH FUNCTION int4recv(internal));
DROP TRANSFORM FOR int LANGUAGE sql;
CREATE FUNCTION waldump_et_func() RETURNS event_trigger LANGUAGE plpgsql AS 'BEGIN END';
CREATE EVENT TRIGGER waldump_et ON ddl_command_start WHEN TAG IN ('CREATE TABLE') EXECUTE FUNCTION waldump_et_func();
ALTER EVENT TRIGGER waldump_et DISABLE;
ALTER EVENT TRIGGER waldump_et RENAME TO waldump_et2;
DROP EVENT TRIGGER waldump_et2;
CREATE TABLE waldump_pubt (a int);
CREATE PUBLICATION waldump_pub FOR TABLE waldump_pubt;
DROP PUBLICATION waldump_pub;
CREATE PUBLICATION waldump_puball FOR ALL TABLES;
DROP PUBLICATION waldump_puball;
CREATE FOREIGN DATA WRAPPER waldump_fdw;
CREATE SERVER waldump_srv FOREIGN DATA WRAPPER waldump_fdw;
DROP SERVER waldump_srv;
CREATE TEXT SEARCH DICTIONARY waldump_dict (TEMPLATE = pg_catalog.simple, STOPWORDS = english);
ALTER TEXT SEARCH DICTIONARY waldump_dict RENAME TO waldump_dict2;
ALTER TEXT SEARCH DICTIONARY waldump_dict2 SET SCHEMA waldump_s1x;
DROP TEXT SEARCH DICTIONARY waldump_s1x.waldump_dict2;
CREATE TEXT SEARCH CONFIGURATION waldump_cfg (PARSER = pg_catalog.default);
ALTER TEXT SEARCH CONFIGURATION waldump_cfg ADD MAPPING FOR asciiword WITH simple;
ALTER TEXT SEARCH CONFIGURATION waldump_cfg RENAME TO waldump_cfg2;
DROP TEXT SEARCH CONFIGURATION waldump_cfg2;
CREATE FUNCTION waldump_am_handler(internal) RETURNS index_am_handler LANGUAGE c AS 'waldump_am_handler';
CREATE ACCESS METHOD waldump_am TYPE INDEX HANDLER waldump_am_handler;
DROP ACCESS METHOD waldump_am;
CREATE COLLATION waldump_coll (locale = 'C');
ALTER COLLATION waldump_coll RENAME TO waldump_coll2;
DROP COLLATION waldump_coll2;
CREATE CONVERSION waldump_conv FOR 'LATIN1' TO 'UTF8' FROM iso8859_1_to_utf8;
ALTER CONVERSION waldump_conv RENAME TO waldump_conv2;
DROP CONVERSION waldump_conv2;
CREATE TYPE waldump_mood AS ENUM ('a', 'b');
ALTER TYPE waldump_mood ADD VALUE 'c';
CREATE TYPE waldump_rng AS RANGE (subtype = int4);
ALTER TYPE waldump_rng RENAME TO waldump_rng2;
DROP TYPE waldump_rng2;
DROP SCHEMA waldump_s2;
` + buildInsertSQL(100) + `
CHECKPOINT;
`
	res, err := util.RunCommand(util.CommandSpec{
		Name:    psqlBin,
		Args:    []string{"-h", host, "-p", port, "-U", "postgres", "-d", "postgres", "-c", workload},
		Env:     psqlEnv,
		Timeout: 30e9, // 30s
	})
	if err != nil || res.ExitCode != 0 {
		t.Fatalf("workload failed (exit=%d): %s\n%s", res.ExitCode, res.Stderr, res.Stdout)
	}

	// Stop cleanly so WAL is flushed.
	if err := c.Stop(cluster.ShutdownSmart); err != nil {
		t.Logf("shutdown warning: %v", err)
	}

	// Locate the data dir and WAL segments. goopg now names segments in
	// native PostgreSQL format (TLI=1 prefix, e.g. 000000010000000000000001 —
	// M0101-0001), so pg_waldump locates each one directly with
	// -p <walDir> <segment-name> and infers the timeline and start LSN from
	// the filename. No alias rewriting or manual -s is needed (the previous
	// version parsed the full 24-hex name with ParseUint, which overflows
	// uint64 and silently skipped every segment → "no WAL segments found").
	walDir := filepath.Join(c.DataDir(), "pg_wal")
	segs := listWALSegments(t, walDir)
	if len(segs) == 0 {
		t.Fatal("no WAL segments found in pg_wal/")
	}
	t.Logf("found %d WAL segment(s); running pg_waldump", len(segs))

	for i, seg := range segs {
		// Skip an all-zero, never-written segment. With wal_init_zero=on
		// (goopg's default) the writer eagerly preallocates the NEXT
		// segment as a full-size zero-filled file when the current one
		// opens, so a trailing all-zero phantom persists across a clean
		// shutdown, exactly as in real PostgreSQL. pg_waldump reads the
		// segment size from the long page header's xlp_seg_size; on an
		// all-zero segment that field is 0 and it fatally reports
		// `invalid WAL segment size ... (0 bytes)`. That is expected for a
		// preallocated tail (real pg_waldump errors identically) and
		// carries no records, so it must not fail the round-trip.
		if segmentIsAllZero(t, filepath.Join(walDir, seg)) {
			t.Logf("segment %d/%d (%s): all-zero preallocated tail, skipping", i+1, len(segs), seg)
			continue
		}
		res, _ := util.RunCommand(util.CommandSpec{
			Name:    waldump,
			Args:    []string{"--quiet", "-p", walDir, seg},
			Env:     []string{"LD_LIBRARY_PATH=" + libDir},
			Timeout: 30e9,
		})
		outStr := strings.TrimSpace(res.Stdout + "\n" + res.Stderr)
		if res.ExitCode != 0 {
			// pg_waldump exits non-zero when it encounters the zero-fill
			// at the end of a partially-written segment ("invalid record
			// length ... got 0") or an "end of WAL" marker. These are
			// expected for the last segment in a live or freshly-stopped
			// cluster. Only treat it as a real failure if the error output
			// indicates a structural problem with the record chain itself
			// (wrong magic, bad CRC, xl_prev mismatch, etc.). The
			// "incorrect prev-link" check is the regression guard for the
			// xl_prev seeding fix in internal/wal/writer.go (detectWritePos).
			isEndOfWAL := strings.Contains(outStr, "invalid record length") ||
				strings.Contains(outStr, "end of WAL") ||
				strings.Contains(outStr, "no start WAL location")
			hasStructuralError := strings.Contains(outStr, "incorrect prev-link") ||
				strings.Contains(outStr, "invalid magic number") ||
				strings.Contains(outStr, "incorrect resource manager") ||
				(strings.Contains(outStr, "error:") && !isEndOfWAL)
			if hasStructuralError {
				t.Errorf("pg_waldump segment %s structural error:\n%s", seg, outStr)
			} else {
				t.Logf("segment %d/%d (%s): pg_waldump OK (end-of-WAL: %s)",
					i+1, len(segs), seg, outStr)
			}
		} else {
			suffix := ""
			if outStr != "" {
				suffix = ": " + outStr
			}
			t.Logf("segment %d/%d (%s): pg_waldump OK%s", i+1, len(segs), seg, suffix)
		}
	}
}

// findPGWaldumpBin locates the pg_waldump binary. Skips if not found.
func findPGWaldumpBin(t *testing.T) string {
	t.Helper()
	if p, err := exec.LookPath("pg_waldump"); err == nil {
		return p
	}
	root := repoRoot(t)
	candidate := filepath.Join(root, "postgres", "local_install", "bin", "pg_waldump")
	if _, err := os.Stat(candidate); err == nil {
		return candidate
	}
	t.Skip("pg_waldump not found in PATH or postgres/local_install/bin")
	return ""
}

// buildInsertSQL returns a SQL string that inserts n rows into wal_test.
func buildInsertSQL(n int) string {
	var b strings.Builder
	b.WriteString("INSERT INTO wal_test(val) VALUES ")
	for i := 0; i < n; i++ {
		if i > 0 {
			b.WriteByte(',')
		}
		fmt.Fprintf(&b, "('row%d')", i+1)
	}
	b.WriteByte(';')
	return b.String()
}
