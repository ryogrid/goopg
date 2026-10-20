package postmaster

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/goopg/goopg/internal/initdb"
)

// TestPgRelationFilepathAndFilenode pins dbsize.c's pg_relation_filepath and
// pg_relation_filenode. pg_relation_filepath did not exist (42883), and
// pg_relation_filenode returned NULL for every index and a view's OID for a
// view.
//
// Expectations, all measured on PG 18.3:
//   - relkinds with storage report pg_class.relfilenode and a path; views,
//     partitioned tables and partitioned indexes report NULL from both;
//   - InvalidOid and an unknown OID give NULL; a name literal goes through
//     regclassin, so a missing name raises 42P01.
//
// The path must also name the file goopg stores the relation in, because the
// upstream TAP tests (pg_amcheck, pg_checksums, recovery) open it to corrupt
// or inspect it.
func TestPgRelationFilepathAndFilenode(t *testing.T) {
	dir := t.TempDir()
	if err := initdb.Init(initdb.Options{DataDir: dir}); err != nil {
		t.Fatalf("initdb.Init: %v", err)
	}
	s := startVMRestartServer(t, dir)
	defer s.close(t)
	db := s.open(t, "postgres")
	defer db.Close()
	ctx := context.Background()
	conn, err := db.Conn(ctx)
	if err != nil {
		t.Fatalf("conn: %v", err)
	}
	defer conn.Close()
	for _, stmt := range []string{
		"CREATE TABLE fp_t (a int PRIMARY KEY, b text)",
		"CREATE VIEW fp_v AS SELECT 1",
		"CREATE SEQUENCE fp_s",
		"CREATE TEMP TABLE fp_tt (a int)",
		"CREATE TABLE fp_pt (a int) PARTITION BY RANGE (a)",
		"CREATE TABLE fp_pt1 PARTITION OF fp_pt FOR VALUES FROM (0) TO (10)",
		"CREATE INDEX fp_pti ON fp_pt (a)",
		"CREATE MATERIALIZED VIEW fp_mv AS SELECT 1 AS x",
	} {
		if _, err := conn.ExecContext(ctx, stmt); err != nil {
			t.Fatalf("%s: %v", stmt, err)
		}
	}

	rows, err := conn.QueryContext(ctx, `SELECT c.relname, c.relkind, c.relfilenode,
		pg_relation_filenode(c.oid), pg_relation_filepath(c.oid)
		FROM pg_class c WHERE c.relname LIKE 'fp\_%' ORDER BY c.relname`)
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	withStorage := map[string]bool{
		"fp_mv": true, "fp_pt1": true, "fp_pt1_a_idx": true, "fp_s": true,
		"fp_t": true, "fp_t_pkey": true, "fp_tt": true,
		"fp_pt": false, "fp_pti": false, "fp_v": false,
	}
	seen := 0
	for rows.Next() {
		var name, kind string
		var relfilenode int64
		var filenode sql.NullInt64
		var path sql.NullString
		if err := rows.Scan(&name, &kind, &relfilenode, &filenode, &path); err != nil {
			t.Fatalf("scan: %v", err)
		}
		want, known := withStorage[name]
		if !known {
			continue
		}
		seen++
		if !want {
			if filenode.Valid || path.Valid {
				t.Errorf("%s (relkind %s): filenode %v, path %v; want NULL for a relation without storage",
					name, kind, filenode, path)
			}
			continue
		}
		if !filenode.Valid || filenode.Int64 != relfilenode {
			t.Errorf("%s (relkind %s): pg_relation_filenode = %v, want pg_class.relfilenode %d", name, kind, filenode, relfilenode)
		}
		if !path.Valid || !strings.HasPrefix(path.String, "base/") {
			t.Errorf("%s (relkind %s): pg_relation_filepath = %v, want base/<db>/<filenode>", name, kind, path)
			continue
		}
		if _, err := os.Stat(filepath.Join(dir, path.String)); err != nil {
			t.Errorf("%s: pg_relation_filepath %q names no file in the data directory: %v", name, path.String, err)
		}
	}
	rows.Close()
	if seen != len(withStorage) {
		t.Errorf("pg_class listed %d of the %d probe relations", seen, len(withStorage))
	}

	var byName, invalidNull, unknownNull, fnInvalidNull, sizeByName bool
	if err := conn.QueryRowContext(ctx, `SELECT
		pg_relation_filepath('fp_t') = pg_relation_filepath('fp_t'::regclass),
		pg_relation_filepath(0) IS NULL, pg_relation_filepath(99999) IS NULL,
		pg_relation_filenode(0) IS NULL,
		pg_relation_size('fp_t') = pg_relation_size('fp_t'::regclass)`).
		Scan(&byName, &invalidNull, &unknownNull, &fnInvalidNull, &sizeByName); err != nil {
		t.Fatalf("argument forms: %v", err)
	}
	if !byName || !invalidNull || !unknownNull || !fnInvalidNull || !sizeByName {
		t.Errorf("by name %v, OID 0 NULL %v, unknown OID NULL %v, filenode(0) NULL %v, pg_relation_size by name %v; want all true",
			byName, invalidNull, unknownNull, fnInvalidNull, sizeByName)
	}

	_, err = conn.ExecContext(ctx, "SELECT pg_relation_filepath('fp_no_such_rel')")
	if err == nil || !strings.Contains(err.Error(), `relation "fp_no_such_rel" does not exist`) {
		t.Errorf("pg_relation_filepath of a missing name: err = %v, want 42P01 relation does not exist", err)
	}
}
