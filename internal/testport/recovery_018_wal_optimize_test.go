package testport

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/goopg/goopg/internal/testutil/tapnode"
)

// TestPort_Recovery018WalOptimize ports
// postgres/src/test/recovery/t/018_wal_optimize.pl (M0149-0006): crash
// recovery of relations whose WAL was skipped (wal_level = minimal, the
// relation created or truncated in the same transaction, committed via
// smgrDoPendingSyncs) against the same steps under wal_level = replica.
func TestPort_Recovery018WalOptimize(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping recovery TAP port in short mode")
	}
	repo := repoRoot(t)
	tapnode.Available(t, repo)
	for _, level := range []string{"minimal", "replica"} {
		t.Run(level, func(t *testing.T) { runWalOptimize(t, repo, level) })
	}
}

func runWalOptimize(t *testing.T, repo, walLevel string) {
	node := tapnode.NewGoopg(t, repo, "node_"+walLevel)
	// Cluster.pm init() for a non-streaming node.
	node.AppendConf("max_wal_senders = 0")
	node.AppendConf("wal_level = " + walLevel)
	node.AppendConf("max_prepared_transactions = 1")
	node.AppendConf("wal_log_hints = on")
	node.AppendConf("wal_skip_threshold = 0")
	node.Start()

	is := func(got, want, name string) {
		t.Helper()
		if got != want {
			t.Errorf("not ok: wal_level = %s, %s: got %q, want %q", walLevel, name, got, want)
		}
	}
	crash := func() {
		t.Helper()
		node.Stop(tapnode.StopImmediate)
		node.Start()
	}
	q := func(sql string) string {
		t.Helper()
		return node.SafePSQL("postgres", sql)
	}

	tablespaceDir := filepath.Join(t.TempDir(), "tablespace_other")
	if err := os.Mkdir(tablespaceDir, 0o700); err != nil {
		t.Fatal(err)
	}

	// Test direct truncation optimization. No tuples.
	q(`
		CREATE TABLE moved (id int);
		INSERT INTO moved VALUES (1);
		CREATE TABLESPACE other LOCATION '` + tablespaceDir + `';
		BEGIN;
		ALTER TABLE moved SET TABLESPACE other;
		CREATE TABLE originated (id int);
		INSERT INTO originated VALUES (1);
		CREATE UNIQUE INDEX ON originated(id) TABLESPACE other;
		COMMIT;`)
	crash()
	is(q("SELECT count(*) FROM moved;"), "1", "CREATE+SET TABLESPACE")
	is(q(`
		INSERT INTO originated VALUES (1) ON CONFLICT (id)
		  DO UPDATE set id = originated.id + 1
		  RETURNING id;`), "2", "CREATE TABLESPACE, CREATE INDEX")

	// Test direct truncation optimization. No tuples.
	q(`
		BEGIN;
		CREATE TABLE trunc (id serial PRIMARY KEY);
		TRUNCATE trunc;
		COMMIT;`)
	crash()
	is(q("SELECT count(*) FROM trunc;"), "0", "TRUNCATE with empty table")

	// Test truncation with inserted tuples within the same transaction.
	// Tuples inserted after the truncation should be seen.
	q(`
		BEGIN;
		CREATE TABLE trunc_ins (id serial PRIMARY KEY);
		INSERT INTO trunc_ins VALUES (DEFAULT);
		TRUNCATE trunc_ins;
		INSERT INTO trunc_ins VALUES (DEFAULT);
		COMMIT;`)
	crash()
	is(q("SELECT count(*), min(id) FROM trunc_ins;"), "1|2", "TRUNCATE INSERT")

	// Same for prepared transaction.
	// Tuples inserted after the truncation should be seen.
	q(`
		BEGIN;
		CREATE TABLE twophase (id serial PRIMARY KEY);
		INSERT INTO twophase VALUES (DEFAULT);
		TRUNCATE twophase;
		INSERT INTO twophase VALUES (DEFAULT);
		PREPARE TRANSACTION 't';
		COMMIT PREPARED 't';`)
	crash()
	is(q("SELECT count(*), min(id) FROM trunc_ins;"), "1|2", "TRUNCATE INSERT PREPARE")

	// Writing WAL at end of xact, instead of syncing.
	q(`
		SET wal_skip_threshold = '1GB';
		BEGIN;
		CREATE TABLE noskip (id serial PRIMARY KEY);
		INSERT INTO noskip (SELECT FROM generate_series(1, 20000) a) ;
		COMMIT;`)
	crash()
	is(q("SELECT count(*) FROM noskip;"), "20000", "end-of-xact WAL")

	// Data file for COPY query in subsequent tests
	copyFile := filepath.Join(t.TempDir(), "copy_data.txt")
	if err := os.WriteFile(copyFile, []byte("20000,30000\n20001,30001\n20002,30002"), 0o644); err != nil {
		t.Fatal(err)
	}

	// Test truncation with inserted tuples using both INSERT and COPY. Tuples
	// inserted after the truncation should be seen.
	q(`
		BEGIN;
		CREATE TABLE ins_trunc (id serial PRIMARY KEY, id2 int);
		INSERT INTO ins_trunc VALUES (DEFAULT, generate_series(1,10000));
		TRUNCATE ins_trunc;
		INSERT INTO ins_trunc (id, id2) VALUES (DEFAULT, 10000);
		COPY ins_trunc FROM '` + copyFile + `' DELIMITER ',';
		INSERT INTO ins_trunc (id, id2) VALUES (DEFAULT, 10000);
		COMMIT;`)
	crash()
	is(q("SELECT count(*) FROM ins_trunc;"), "5", "TRUNCATE COPY INSERT")

	// Test truncation with inserted tuples using COPY. Tuples copied after
	// the truncation should be seen.
	q(`
		BEGIN;
		CREATE TABLE trunc_copy (id serial PRIMARY KEY, id2 int);
		INSERT INTO trunc_copy VALUES (DEFAULT, generate_series(1,3000));
		TRUNCATE trunc_copy;
		COPY trunc_copy FROM '` + copyFile + `' DELIMITER ',';
		COMMIT;`)
	crash()
	is(q("SELECT count(*) FROM trunc_copy;"), "3", "TRUNCATE COPY")

	// Like previous test, but rollback SET TABLESPACE in a subtransaction.
	q(`
		BEGIN;
		CREATE TABLE spc_abort (id serial PRIMARY KEY, id2 int);
		INSERT INTO spc_abort VALUES (DEFAULT, generate_series(1,3000));
		TRUNCATE spc_abort;
		SAVEPOINT s;
		  ALTER TABLE spc_abort SET TABLESPACE other; ROLLBACK TO s;
		COPY spc_abort FROM '` + copyFile + `' DELIMITER ',';
		COMMIT;`)
	crash()
	is(q("SELECT count(*) FROM spc_abort;"), "3", "SET TABLESPACE abort subtransaction")

	// in different subtransaction patterns
	q(`
		BEGIN;
		CREATE TABLE spc_commit (id serial PRIMARY KEY, id2 int);
		INSERT INTO spc_commit VALUES (DEFAULT, generate_series(1,3000));
		TRUNCATE spc_commit;
		SAVEPOINT s; ALTER TABLE spc_commit SET TABLESPACE other; RELEASE s;
		COPY spc_commit FROM '` + copyFile + `' DELIMITER ',';
		COMMIT;`)
	crash()
	is(q("SELECT count(*) FROM spc_commit;"), "3", "SET TABLESPACE commit subtransaction")

	q(`
		BEGIN;
		CREATE TABLE spc_nest (id serial PRIMARY KEY, id2 int);
		INSERT INTO spc_nest VALUES (DEFAULT, generate_series(1,3000));
		TRUNCATE spc_nest;
		SAVEPOINT s;
			ALTER TABLE spc_nest SET TABLESPACE other;
			SAVEPOINT s2;
				ALTER TABLE spc_nest SET TABLESPACE pg_default;
			ROLLBACK TO s2;
			SAVEPOINT s2;
				ALTER TABLE spc_nest SET TABLESPACE pg_default;
			RELEASE s2;
		ROLLBACK TO s;
		COPY spc_nest FROM '` + copyFile + `' DELIMITER ',';
		COMMIT;`)
	crash()
	is(q("SELECT count(*) FROM spc_nest;"), "3", "SET TABLESPACE nested subtransaction")

	q(`
		CREATE TABLE spc_hint (id int);
		INSERT INTO spc_hint VALUES (1);
		BEGIN;
		ALTER TABLE spc_hint SET TABLESPACE other;
		CHECKPOINT;
		SELECT * FROM spc_hint;  -- set hint bit
		INSERT INTO spc_hint VALUES (2);
		COMMIT;`)
	crash()
	is(q("SELECT count(*) FROM spc_hint;"), "2", "SET TABLESPACE, hint bit")

	q(`
		BEGIN;
		CREATE TABLE idx_hint (c int PRIMARY KEY);
		SAVEPOINT q; INSERT INTO idx_hint VALUES (1); ROLLBACK TO q;
		CHECKPOINT;
		INSERT INTO idx_hint VALUES (1);  -- set index hint bit
		INSERT INTO idx_hint VALUES (2);
		COMMIT;`)
	crash()
	node.PSQLExit("postgres", "")
	_, stderr, exit := node.PSQLExit("postgres", "INSERT INTO idx_hint VALUES (2);")
	if exit != 3 {
		t.Errorf("not ok: wal_level = %s, unique index LP_DEAD: exit %d, want 3", walLevel, exit)
	}
	if !strings.Contains(stderr, "violates unique") {
		t.Errorf("not ok: wal_level = %s, unique index LP_DEAD message: %q", walLevel, stderr)
	}

	// UPDATE touches two buffers for one row.
	q(`
		BEGIN;
		CREATE TABLE upd (id serial PRIMARY KEY, id2 int);
		INSERT INTO upd (id, id2) VALUES (DEFAULT, generate_series(1,10000));
		COPY upd FROM '` + copyFile + `' DELIMITER ',';
		UPDATE upd SET id2 = id2 + 1;
		DELETE FROM upd;
		COMMIT;`)
	crash()
	is(q("SELECT count(*) FROM upd;"), "0", "UPDATE touches two buffers for one row")

	// Test consistency of COPY with INSERT for table created in the same
	// transaction.
	q(`
		BEGIN;
		CREATE TABLE ins_copy (id serial PRIMARY KEY, id2 int);
		INSERT INTO ins_copy VALUES (DEFAULT, 1);
		COPY ins_copy FROM '` + copyFile + `' DELIMITER ',';
		COMMIT;`)
	crash()
	is(q("SELECT count(*) FROM ins_copy;"), "4", "INSERT COPY")

	// Test consistency of COPY that inserts more to the same table using
	// triggers. If the INSERTS from the trigger go to the same block data
	// is copied to, and the INSERTs are WAL-logged, WAL replay will fail when
	// it tries to replay the WAL record but the "before" image doesn't match,
	// because not all changes were WAL-logged.
	q(`
		BEGIN;
		CREATE TABLE ins_trig (id serial PRIMARY KEY, id2 text);
		CREATE FUNCTION ins_trig_before_row_trig() RETURNS trigger
		  LANGUAGE plpgsql as $$
		  BEGIN
			IF new.id2 NOT LIKE 'triggered%' THEN
			  INSERT INTO ins_trig
				VALUES (DEFAULT, 'triggered row before' || NEW.id2);
			END IF;
			RETURN NEW;
		  END; $$;
		CREATE FUNCTION ins_trig_after_row_trig() RETURNS trigger
		  LANGUAGE plpgsql as $$
		  BEGIN
			IF new.id2 NOT LIKE 'triggered%' THEN
			  INSERT INTO ins_trig
				VALUES (DEFAULT, 'triggered row after' || NEW.id2);
			END IF;
			RETURN NEW;
		  END; $$;
		CREATE TRIGGER ins_trig_before_row_insert
		  BEFORE INSERT ON ins_trig
		  FOR EACH ROW EXECUTE PROCEDURE ins_trig_before_row_trig();
		CREATE TRIGGER ins_trig_after_row_insert
		  AFTER INSERT ON ins_trig
		  FOR EACH ROW EXECUTE PROCEDURE ins_trig_after_row_trig();
		COPY ins_trig FROM '` + copyFile + `' DELIMITER ',';
		COMMIT;`)
	crash()
	is(q("SELECT count(*) FROM ins_trig;"), "9", "COPY with INSERT triggers")

	// Test consistency of INSERT, COPY and TRUNCATE in same transaction block
	// with TRUNCATE triggers.
	q(`
		BEGIN;
		CREATE TABLE trunc_trig (id serial PRIMARY KEY, id2 text);
		CREATE FUNCTION trunc_trig_before_stat_trig() RETURNS trigger
		  LANGUAGE plpgsql as $$
		  BEGIN
			INSERT INTO trunc_trig VALUES (DEFAULT, 'triggered stat before');
			RETURN NULL;
		  END; $$;
		CREATE FUNCTION trunc_trig_after_stat_trig() RETURNS trigger
		  LANGUAGE plpgsql as $$
		  BEGIN
			INSERT INTO trunc_trig VALUES (DEFAULT, 'triggered stat before');
			RETURN NULL;
		  END; $$;
		CREATE TRIGGER trunc_trig_before_stat_truncate
		  BEFORE TRUNCATE ON trunc_trig
		  FOR EACH STATEMENT EXECUTE PROCEDURE trunc_trig_before_stat_trig();
		CREATE TRIGGER trunc_trig_after_stat_truncate
		  AFTER TRUNCATE ON trunc_trig
		  FOR EACH STATEMENT EXECUTE PROCEDURE trunc_trig_after_stat_trig();
		INSERT INTO trunc_trig VALUES (DEFAULT, 1);
		TRUNCATE trunc_trig;
		COPY trunc_trig FROM '` + copyFile + `' DELIMITER ',';
		COMMIT;`)
	crash()
	is(q("SELECT count(*) FROM trunc_trig;"), "4", "TRUNCATE COPY with TRUNCATE triggers")

	// Test redo of temp table creation.
	q(`CREATE TEMP TABLE temp (id serial PRIMARY KEY, id2 text);`)
	crash()
	checkOrphanRelfilenodes(t, node, "wal_level = "+walLevel+", no orphan relfilenode remains")
}

// checkOrphanRelfilenodes is the test's check_orphan_relfilenodes: every
// numeric file under base/<postgres oid>/ is the relfilenode of a pg_class
// row in the default tablespace, and the reverse.
func checkOrphanRelfilenodes(t *testing.T, node *tapnode.Node, name string) {
	t.Helper()
	dbOid := node.SafePSQL("postgres", "SELECT oid FROM pg_database WHERE datname = 'postgres'")
	prefix := "base/" + dbOid + "/"
	referenced := node.SafePSQL("postgres", `
	   SELECT pg_relation_filepath(oid) FROM pg_class
	   WHERE reltablespace = 0 AND relpersistence <> 't' AND
	   pg_relation_filepath(oid) IS NOT NULL;`)
	ents, err := os.ReadDir(filepath.Join(node.DataDir(), prefix))
	if err != nil {
		t.Fatal(err)
	}
	digits := regexp.MustCompile(`^[0-9]+$`)
	var onDisk []string
	for _, e := range ents {
		if digits.MatchString(e.Name()) {
			onDisk = append(onDisk, prefix+e.Name())
		}
	}
	want := strings.Split(referenced, "\n")
	sort.Strings(onDisk)
	sort.Strings(want)
	if strings.Join(onDisk, "\n") != strings.Join(want, "\n") {
		extraDisk, extraCat := diffSorted(onDisk, want)
		t.Errorf("not ok: %s: files without pg_class %v; pg_class without files %v", name, extraDisk, extraCat)
	}
}

func diffSorted(a, b []string) (onlyA, onlyB []string) {
	in := func(s []string) map[string]bool {
		m := map[string]bool{}
		for _, x := range s {
			m[x] = true
		}
		return m
	}
	ma, mb := in(a), in(b)
	for _, x := range a {
		if !mb[x] {
			onlyA = append(onlyA, x)
		}
	}
	for _, x := range b {
		if !ma[x] {
			onlyB = append(onlyB, x)
		}
	}
	return onlyA, onlyB
}
