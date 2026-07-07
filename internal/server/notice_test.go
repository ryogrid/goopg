package server

import (
	"context"
	"database/sql"
	"strings"
	"sync"
	"testing"

	"github.com/goopg/goopg/internal/protocol"
	"github.com/lib/pq"
	_ "github.com/lib/pq"
)

// TestNoticeResponseOnTrigger verifies that goopg sends a 'N' (NoticeResponse)
// frame when a PL/pgSQL trigger fires RAISE NOTICE. This is the server-side
// counterpart to the client-side notice capture verification.
func TestNoticeResponseOnTrigger(t *testing.T) {
	addr, cat, stop := startCopyExecServer(t)
	defer stop()
	_ = cat

	conn := dialAndComplete(t, addr)
	defer conn.Close()

	// Create table + trigger function + trigger
	setup := []string{
		`CREATE TABLE noticetest (id int, val text)`,
		`INSERT INTO noticetest VALUES (1, 'row1')`,
		`CREATE FUNCTION noticetest_trig_fn() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
  RAISE NOTICE 'trigger fired';
  RETURN OLD;
END
$$`,
		`CREATE TRIGGER noticetest_trig BEFORE DELETE ON noticetest
FOR EACH ROW EXECUTE FUNCTION noticetest_trig_fn()`,
	}
	for _, sql := range setup {
		writeQuery(t, conn, sql)
		frames := readUntilReady(t, conn)
		for _, f := range frames {
			if f.Type == protocol.MsgErrorResponse {
				t.Fatalf("setup SQL %q failed: %s", sql, string(f.Payload))
			}
		}
	}

	// Run DELETE that should fire the trigger
	writeQuery(t, conn, `DELETE FROM noticetest WHERE id = 1`)
	frames := readUntilReady(t, conn)

	// Check for NoticeResponse
	var noticeFrames []protocol.Frame
	for _, f := range frames {
		if f.Type == protocol.MsgNoticeResponse {
			noticeFrames = append(noticeFrames, f)
		}
		if f.Type == protocol.MsgErrorResponse {
			t.Fatalf("DELETE failed: %s", string(f.Payload))
		}
	}

	if len(noticeFrames) == 0 {
		t.Log("Frame types received:")
		for _, f := range frames {
			t.Logf("  %c (%s)", f.Type, string(f.Payload[:min(len(f.Payload), 50)]))
		}
		t.Error("no NoticeResponse ('N') frame received; trigger RAISE NOTICE did not produce a notice")
	} else {
		t.Logf("SUCCESS: received %d NoticeResponse frame(s)", len(noticeFrames))
	}
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// TestNoticeCapturePQ verifies that pq's notice handler fires when goopg
// sends a NoticeResponse. This end-to-end test confirms the client-side
// notice capture path works correctly.
func TestNoticeCapturePQ(t *testing.T) {
	addr, cat, stop := startCopyExecServer(t)
	defer stop()
	_ = cat

	// Build DSN from addr (format: "host:port")
	colonIdx := strings.LastIndex(addr, ":")
	if colonIdx < 0 {
		t.Fatalf("unexpected addr format: %s", addr)
	}
	host, port := addr[:colonIdx], addr[colonIdx+1:]
	dsn := "host=" + host + " port=" + port + " user=postgres dbname=postgres sslmode=disable"

	// Use pq.NewConnector + pq.ConnectorWithNoticeHandler
	base, err := pq.NewConnector(dsn)
	if err != nil {
		t.Skipf("pq.NewConnector failed: %v", err)
	}
	var noticesMu sync.Mutex
	var notices []string
	withNotice := pq.ConnectorWithNoticeHandler(base, func(n *pq.Error) {
		noticesMu.Lock()
		notices = append(notices, n.Message)
		noticesMu.Unlock()
	})
	db := sql.OpenDB(withNotice)
	defer db.Close()

	ctx := context.Background()
	conn, err := db.Conn(ctx)
	if err != nil {
		t.Fatalf("Conn: %v", err)
	}
	defer conn.Close()

	// Setup
	for _, q := range []string{
		`CREATE TABLE pq_notice_test (id int, val text)`,
		`INSERT INTO pq_notice_test VALUES (1, 'row1')`,
		`CREATE FUNCTION pq_notice_trig_fn() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
  RAISE NOTICE 'pq notice works';
  RETURN OLD;
END
$$`,
		`CREATE TRIGGER pq_notice_trig BEFORE DELETE ON pq_notice_test
FOR EACH ROW EXECUTE FUNCTION pq_notice_trig_fn()`,
	} {
		rows, err := conn.QueryContext(ctx, q)
		if err != nil {
			t.Skipf("setup %q: %v", q, err)
		}
		rows.Close()
	}

	// Delete to fire trigger
	rows, err := conn.QueryContext(ctx, `DELETE FROM pq_notice_test WHERE id = 1`)
	if err != nil {
		t.Fatalf("DELETE: %v", err)
	}
	rows.Close()

	noticesMu.Lock()
	got := append([]string(nil), notices...)
	noticesMu.Unlock()

	if len(got) == 0 {
		t.Error("no notices captured by pq — notice handler did not fire")
	} else {
		t.Logf("captured notices: %v", got)
	}
}

// TestNoticeCaptureUpdateTrigger verifies that BEFORE UPDATE triggers with
// RAISE NOTICE work correctly — both the trigger firing and pq notice capture.
func TestNoticeCaptureUpdateTrigger(t *testing.T) {
	addr, _, stop := startCopyExecServer(t)
	defer stop()

	colonIdx := strings.LastIndex(addr, ":")
	if colonIdx < 0 { t.Fatalf("addr: %s", addr) }
	host, port := addr[:colonIdx], addr[colonIdx+1:]
	dsn := "host=" + host + " port=" + port + " user=postgres dbname=postgres sslmode=disable"

	base, err := pq.NewConnector(dsn)
	if err != nil { t.Skipf("pq.NewConnector: %v", err) }
	var noticeMu sync.Mutex
	var notices []string
	withNotice := pq.ConnectorWithNoticeHandler(base, func(n *pq.Error) {
		noticeMu.Lock()
		notices = append(notices, n.Message)
		noticeMu.Unlock()
	})
	db := sql.OpenDB(withNotice)
	defer db.Close()

	ctx := context.Background()
	conn, err := db.Conn(ctx)
	if err != nil { t.Fatal(err) }
	defer conn.Close()

	for _, q := range []string{
		`CREATE TABLE upd_trig_test (key int primary key, val text)`,
		`INSERT INTO upd_trig_test VALUES (1, 'old')`,
		`CREATE FUNCTION upd_trig_fn() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE NOTICE 'updated'; RETURN NEW; END $$`,
		`CREATE TRIGGER upd_trig BEFORE UPDATE ON upd_trig_test FOR EACH ROW EXECUTE FUNCTION upd_trig_fn()`,
	} {
		rows, err := conn.QueryContext(ctx, q)
		if err != nil { t.Skipf("setup %q: %v", q, err) }
		rows.Close()
	}

	noticeMu.Lock(); notices = nil; noticeMu.Unlock()

	rows, err := conn.QueryContext(ctx, `UPDATE upd_trig_test SET val = 'new' WHERE key = 1`)
	if err != nil { t.Fatalf("UPDATE: %v", err) }
	rows.Close()

	noticeMu.Lock(); got := append([]string(nil), notices...); noticeMu.Unlock()
	t.Logf("notices: %v", got)
	if len(got) == 0 {
		t.Error("no notice — BEFORE UPDATE trigger RAISE NOTICE not working")
	}
}


// TestNoticeRaisePgTypeofRendersTypeName pins a regression surfaced by the
// M-NIGHTLY eval-plan-qual(-trigger) isolation spec failures
// (AI-20260707-000712-002/-003): once pg_typeof()/::regtype started
// evaluating to an OID-backed Datum (M0122-0005), plpgsql's RAISE NOTICE
// format-arg substitution printed the bare pg_type OID (e.g. "25") instead
// of resolving it to the display name ("text") the way a plain `SELECT
// pg_typeof(...)` does.
func TestNoticeRaisePgTypeofRendersTypeName(t *testing.T) {
	addr, _, stop := startCopyExecServer(t)
	defer stop()

	dsn := "host=" + addr[:strings.LastIndex(addr, ":")] + " port=" + addr[strings.LastIndex(addr, ":")+1:] + " user=postgres dbname=postgres sslmode=disable"
	base, err := pq.NewConnector(dsn)
	if err != nil {
		t.Skipf("pq.NewConnector: %v", err)
	}
	var noticeMu sync.Mutex
	var notices []string
	withNotice := pq.ConnectorWithNoticeHandler(base, func(n *pq.Error) {
		noticeMu.Lock()
		notices = append(notices, n.Message)
		noticeMu.Unlock()
	})
	db := sql.OpenDB(withNotice)
	defer db.Close()

	ctx := context.Background()
	conn, err := db.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()

	setup := `CREATE FUNCTION pg_typeof_raise_probe() RETURNS void LANGUAGE plpgsql AS $$
BEGIN
  RAISE NOTICE '%: % %', pg_typeof(1::int4), pg_typeof('x'::text), 'text'::regtype;
END
$$`
	rows, err := conn.QueryContext(ctx, setup)
	if err != nil {
		t.Fatalf("setup: %v", err)
	}
	rows.Close()

	noticeMu.Lock()
	notices = nil
	noticeMu.Unlock()

	rows, err = conn.QueryContext(ctx, `SELECT pg_typeof_raise_probe()`)
	if err != nil {
		t.Fatalf("SELECT pg_typeof_raise_probe(): %v", err)
	}
	rows.Close()

	noticeMu.Lock()
	got := append([]string(nil), notices...)
	noticeMu.Unlock()

	if len(got) != 1 {
		t.Fatalf("notices = %v, want exactly 1", got)
	}
	want := "integer: text text"
	if got[0] != want {
		t.Errorf("notice = %q, want %q (pg_typeof()/::regtype must render the type name, not the raw pg_type OID, inside RAISE NOTICE format args)", got[0], want)
	}
}

// TestPartitionChildTriggerFiresOnParentUpdate (M0100-0005o) verifies that
// when a BEFORE UPDATE trigger is defined on a partition child but the UPDATE
// statement targets the partitioned parent, the child's trigger still fires
// for rows that route to that child. partition-key-update-1.spec depends on
// this — `footrg_mod_a` is defined on `footrg1` (the leaf partition), and the
// `footrg` UPDATE must observe the trigger's NEW.a rewrite.
func TestPartitionChildTriggerFiresOnParentUpdate(t *testing.T) {
	addr, _, stop := startCopyExecServer(t)
	defer stop()

	colonIdx := strings.LastIndex(addr, ":")
	if colonIdx < 0 {
		t.Fatalf("addr: %s", addr)
	}
	host, port := addr[:colonIdx], addr[colonIdx+1:]
	dsn := "host=" + host + " port=" + port + " user=postgres dbname=postgres sslmode=disable"

	base, err := pq.NewConnector(dsn)
	if err != nil {
		t.Skipf("pq.NewConnector: %v", err)
	}
	var noticeMu sync.Mutex
	var notices []string
	withNotice := pq.ConnectorWithNoticeHandler(base, func(n *pq.Error) {
		noticeMu.Lock()
		notices = append(notices, n.Message)
		noticeMu.Unlock()
	})
	db := sql.OpenDB(withNotice)
	defer db.Close()

	ctx := context.Background()
	conn, err := db.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()

	for _, q := range []string{
		`CREATE TABLE footrg_part (a int, b text) PARTITION BY LIST(a)`,
		`CREATE TABLE footrg_part1 PARTITION OF footrg_part FOR VALUES IN (1)`,
		`CREATE TABLE footrg_part2 PARTITION OF footrg_part FOR VALUES IN (2)`,
		`INSERT INTO footrg_part VALUES (1, 'ABC')`,
		`CREATE FUNCTION footrg_part_trig_fn() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE NOTICE 'before-update child'; RETURN NEW; END $$`,
		`CREATE TRIGGER footrg_part_trig BEFORE UPDATE ON footrg_part1 FOR EACH ROW EXECUTE FUNCTION footrg_part_trig_fn()`,
	} {
		rows, err := conn.QueryContext(ctx, q)
		if err != nil {
			t.Skipf("setup %q: %v", q, err)
		}
		rows.Close()
	}

	noticeMu.Lock()
	notices = nil
	noticeMu.Unlock()

	// Issue the UPDATE against the partitioned PARENT. The matching row lives
	// in footrg_part1; the partition-child trigger must observe it.
	rows, err := conn.QueryContext(ctx, `UPDATE footrg_part SET b = 'EFG' WHERE a = 1`)
	if err != nil {
		t.Fatalf("UPDATE on partitioned parent: %v", err)
	}
	rows.Close()

	noticeMu.Lock()
	got := append([]string(nil), notices...)
	noticeMu.Unlock()
	t.Logf("notices: %v", got)
	if len(got) == 0 {
		t.Fatal("partition-child BEFORE UPDATE trigger did not fire on UPDATE of partitioned parent (M0100-0005o regression)")
	}
}

// TestPartitionChildTriggerFiresOnParentDelete (M0100-0005o) mirror of the
// UPDATE case for the DELETE path — DELETE FROM partitioned-parent must
// trigger the child's BEFORE DELETE trigger for rows routed to the child.
func TestPartitionChildTriggerFiresOnParentDelete(t *testing.T) {
	addr, _, stop := startCopyExecServer(t)
	defer stop()

	colonIdx := strings.LastIndex(addr, ":")
	if colonIdx < 0 {
		t.Fatalf("addr: %s", addr)
	}
	host, port := addr[:colonIdx], addr[colonIdx+1:]
	dsn := "host=" + host + " port=" + port + " user=postgres dbname=postgres sslmode=disable"

	base, err := pq.NewConnector(dsn)
	if err != nil {
		t.Skipf("pq.NewConnector: %v", err)
	}
	var noticeMu sync.Mutex
	var notices []string
	withNotice := pq.ConnectorWithNoticeHandler(base, func(n *pq.Error) {
		noticeMu.Lock()
		notices = append(notices, n.Message)
		noticeMu.Unlock()
	})
	db := sql.OpenDB(withNotice)
	defer db.Close()

	ctx := context.Background()
	conn, err := db.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()

	for _, q := range []string{
		`CREATE TABLE footrg_del (a int, b text) PARTITION BY LIST(a)`,
		`CREATE TABLE footrg_del1 PARTITION OF footrg_del FOR VALUES IN (1)`,
		`CREATE TABLE footrg_del2 PARTITION OF footrg_del FOR VALUES IN (2)`,
		`INSERT INTO footrg_del VALUES (1, 'ABC')`,
		`CREATE FUNCTION footrg_del_trig_fn() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE NOTICE 'before-delete child'; RETURN OLD; END $$`,
		`CREATE TRIGGER footrg_del_trig BEFORE DELETE ON footrg_del1 FOR EACH ROW EXECUTE FUNCTION footrg_del_trig_fn()`,
	} {
		rows, err := conn.QueryContext(ctx, q)
		if err != nil {
			t.Skipf("setup %q: %v", q, err)
		}
		rows.Close()
	}

	noticeMu.Lock()
	notices = nil
	noticeMu.Unlock()

	rows, err := conn.QueryContext(ctx, `DELETE FROM footrg_del WHERE a = 1`)
	if err != nil {
		t.Fatalf("DELETE on partitioned parent: %v", err)
	}
	rows.Close()

	noticeMu.Lock()
	got := append([]string(nil), notices...)
	noticeMu.Unlock()
	t.Logf("notices: %v", got)
	if len(got) == 0 {
		t.Fatal("partition-child BEFORE DELETE trigger did not fire on DELETE from partitioned parent (M0100-0005o regression)")
	}
}

// TestTriggerDrivenPartitionKeyRewriteMovesRowAcrossPartitions
// (M0100-0005p) verifies that when a BEFORE UPDATE trigger on a leaf
// partition rewrites the partition-key column (NEW.a := 2), the
// resulting row is routed to the destination partition AND the source
// slot in the old partition is no longer visible to subsequent reads.
// This is the precondition for the moved-partition concurrency error
// (partition-key-update-1.spec); without it, the M0100-0005n EPQ check
// has nothing to detect on the old slot.
func TestTriggerDrivenPartitionKeyRewriteMovesRowAcrossPartitions(t *testing.T) {
	addr, _, stop := startCopyExecServer(t)
	defer stop()

	colonIdx := strings.LastIndex(addr, ":")
	if colonIdx < 0 {
		t.Fatalf("addr: %s", addr)
	}
	host, port := addr[:colonIdx], addr[colonIdx+1:]
	dsn := "host=" + host + " port=" + port + " user=postgres dbname=postgres sslmode=disable"

	db, err := sql.Open("postgres", dsn)
	if err != nil {
		t.Skipf("sql.Open: %v", err)
	}
	defer db.Close()

	ctx := context.Background()
	conn, err := db.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()

	for _, q := range []string{
		`CREATE TABLE footrg_mv (a int, b text) PARTITION BY LIST(a)`,
		`CREATE TABLE footrg_mv1 PARTITION OF footrg_mv FOR VALUES IN (1)`,
		`CREATE TABLE footrg_mv2 PARTITION OF footrg_mv FOR VALUES IN (2)`,
		`INSERT INTO footrg_mv VALUES (1, 'ABC')`,
		`CREATE FUNCTION footrg_mv_trig_fn() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN NEW.a = 2; RETURN NEW; END $$`,
		`CREATE TRIGGER footrg_mv_trig BEFORE UPDATE ON footrg_mv1 FOR EACH ROW EXECUTE FUNCTION footrg_mv_trig_fn()`,
	} {
		rows, err := conn.QueryContext(ctx, q)
		if err != nil {
			t.Skipf("setup %q: %v", q, err)
		}
		rows.Close()
	}

	if _, err := conn.ExecContext(ctx, `UPDATE footrg_mv SET b = 'XYZ' WHERE a = 1`); err != nil {
		t.Fatalf("UPDATE: %v", err)
	}

	// Row should now live in footrg_mv2 with a=2.
	var got2 int
	if err := conn.QueryRowContext(ctx, `SELECT count(*) FROM footrg_mv2`).Scan(&got2); err != nil {
		t.Fatalf("count footrg_mv2: %v", err)
	}
	if got2 != 1 {
		t.Errorf("footrg_mv2 row count = %d, want 1 (trigger should have routed the row to a=2 partition)", got2)
	}

	// Source partition should no longer hold the row.
	var got1 int
	if err := conn.QueryRowContext(ctx, `SELECT count(*) FROM footrg_mv1`).Scan(&got1); err != nil {
		t.Fatalf("count footrg_mv1: %v", err)
	}
	if got1 != 0 {
		t.Errorf("footrg_mv1 row count = %d, want 0 (old slot must be invisible after cross-partition move)", got1)
	}

	// Parent should still report exactly 1 row.
	var gotP int
	if err := conn.QueryRowContext(ctx, `SELECT count(*) FROM footrg_mv`).Scan(&gotP); err != nil {
		t.Fatalf("count footrg_mv: %v", err)
	}
	if gotP != 1 {
		t.Errorf("footrg_mv (parent) row count = %d, want 1", gotP)
	}
}


// TestCrossPartitionUpdateFiresBeforeDeleteOnSourcePartition pins
// M0100-0005aa: a cross-partition UPDATE (DELETE+INSERT internally) must
// fire any BEFORE DELETE row trigger defined on the source leaf
// partition, mirroring upstream ExecCrossPartitionUpdate.  The trigger
// body legitimately mutates OLD and reads it back via embedded SQL —
// partition-key-update-4.spec's `OLD.b = OLD.b || ' trigger'; INSERT
// INTO triglog select OLD.*` shape.
func TestCrossPartitionUpdateFiresBeforeDeleteOnSourcePartition(t *testing.T) {
	addr, _, stop := startCopyExecServer(t)
	defer stop()

	colonIdx := strings.LastIndex(addr, ":")
	if colonIdx < 0 {
		t.Fatalf("addr: %s", addr)
	}
	host, port := addr[:colonIdx], addr[colonIdx+1:]
	dsn := "host=" + host + " port=" + port + " user=postgres dbname=postgres sslmode=disable"

	db, err := sql.Open("postgres", dsn)
	if err != nil {
		t.Skipf("sql.Open: %v", err)
	}
	defer db.Close()

	ctx := context.Background()
	conn, err := db.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()

	for _, q := range []string{
		`CREATE TABLE xpd_foo (a int, b text) PARTITION BY LIST(a)`,
		`CREATE TABLE xpd_foo1 PARTITION OF xpd_foo FOR VALUES IN (1)`,
		`CREATE TABLE xpd_foo2 PARTITION OF xpd_foo FOR VALUES IN (2)`,
		`CREATE TABLE xpd_triglog (a int, b text)`,
		`INSERT INTO xpd_foo VALUES (1, 'ABC')`,
		`CREATE FUNCTION xpd_trig_fn() RETURNS trigger LANGUAGE plpgsql AS $$
		   BEGIN
		     OLD.b = OLD.b || ' trigger';
		     INSERT INTO xpd_triglog select OLD.*;
		     RETURN OLD;
		   END $$`,
		`CREATE TRIGGER xpd_trig BEFORE DELETE ON xpd_foo1 FOR EACH ROW EXECUTE FUNCTION xpd_trig_fn()`,
	} {
		rows, err := conn.QueryContext(ctx, q)
		if err != nil {
			t.Fatalf("setup %q: %v", q, err)
		}
		rows.Close()
	}

	// Cross-partition UPDATE moves (1,'ABC') from xpd_foo1 to xpd_foo2.
	// BEFORE DELETE on xpd_foo1 must fire and insert (1,'ABC trigger')
	// into xpd_triglog.
	if _, err := conn.ExecContext(ctx, `UPDATE xpd_foo SET a = 2, b = b || ' update1' WHERE b like '%ABC%'`); err != nil {
		t.Fatalf("UPDATE: %v", err)
	}

	var logA int
	var logB string
	if err := conn.QueryRowContext(ctx, `SELECT a, b FROM xpd_triglog`).Scan(&logA, &logB); err != nil {
		t.Fatalf("scan triglog: %v", err)
	}
	if logA != 1 || logB != "ABC trigger" {
		t.Errorf("triglog row = (%d, %q), want (1, %q) — BEFORE DELETE trigger did not fire or OLD mutation did not propagate to embedded INSERT",
			logA, logB, "ABC trigger")
	}

	// Source partition empty, destination has the moved row.
	var cnt1, cnt2 int
	if err := conn.QueryRowContext(ctx, `SELECT count(*) FROM xpd_foo1`).Scan(&cnt1); err != nil {
		t.Fatalf("count foo1: %v", err)
	}
	if err := conn.QueryRowContext(ctx, `SELECT count(*) FROM xpd_foo2`).Scan(&cnt2); err != nil {
		t.Fatalf("count foo2: %v", err)
	}
	if cnt1 != 0 || cnt2 != 1 {
		t.Errorf("partition counts: foo1=%d foo2=%d, want 0 and 1", cnt1, cnt2)
	}
}

// TestNoticeCaptureUpdateTriggerExplicitTx tests BEFORE UPDATE trigger NOTICE
// within an explicit transaction (BEGIN), matching the isolation test scenario.
func TestNoticeCaptureUpdateTriggerExplicitTx(t *testing.T) {
	addr, _, stop := startCopyExecServer(t)
	defer stop()

	colonIdx := strings.LastIndex(addr, ":")
	if colonIdx < 0 { t.Fatalf("addr: %s", addr) }
	host, port := addr[:colonIdx], addr[colonIdx+1:]
	dsn := "host=" + host + " port=" + port + " user=postgres dbname=postgres sslmode=disable"

	base, err := pq.NewConnector(dsn)
	if err != nil { t.Skipf("pq.NewConnector: %v", err) }
	var noticeMu sync.Mutex
	var notices []string
	withNotice := pq.ConnectorWithNoticeHandler(base, func(n *pq.Error) {
		noticeMu.Lock()
		notices = append(notices, n.Message)
		noticeMu.Unlock()
	})
	db := sql.OpenDB(withNotice)
	defer db.Close()

	ctx := context.Background()
	conn, err := db.Conn(ctx)
	if err != nil { t.Fatal(err) }
	defer conn.Close()

	for _, q := range []string{
		`CREATE TABLE upd_tx_test (key int primary key, balance int, status text, val text)`,
		`INSERT INTO upd_tx_test VALUES (1, 160, 's1', 'setup')`,
		`CREATE FUNCTION upd_tx_trig_fn() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE NOTICE 'Update: % -> %', OLD, NEW; RETURN NEW; END $$`,
		`CREATE TRIGGER upd_tx_trig BEFORE INSERT OR UPDATE OR DELETE ON upd_tx_test FOR EACH ROW EXECUTE FUNCTION upd_tx_trig_fn()`,
	} {
		rows, err := conn.QueryContext(ctx, q)
		if err != nil { t.Skipf("setup %q: %v", q, err) }
		rows.Close()
	}

	// Begin explicit transaction (matches isolation test session setup)
	rows, err := conn.QueryContext(ctx, `BEGIN ISOLATION LEVEL READ COMMITTED`)
	if err != nil { t.Fatalf("BEGIN: %v", err) }
	rows.Close()

	noticeMu.Lock(); notices = nil; noticeMu.Unlock()

	rows, err = conn.QueryContext(ctx, `UPDATE upd_tx_test t SET balance = balance + 10, val = t.val || ' updated' WHERE t.key = 1`)
	if err != nil { t.Fatalf("UPDATE: %v", err) }
	rows.Close()

	noticeMu.Lock(); got := append([]string(nil), notices...); noticeMu.Unlock()
	t.Logf("notices within explicit tx: %v", got)
	if len(got) == 0 {
		t.Error("no notice in explicit transaction — BEFORE UPDATE trigger not working with explicit tx")
	}
}

// TestNoticeSeparateConnections tests BEFORE UPDATE trigger NOTICE where
// the trigger setup is done on a different connection (mimicking isolation test setup).
func TestNoticeSeparateConnections(t *testing.T) {
	addr, _, stop := startCopyExecServer(t)
	defer stop()

	colonIdx := strings.LastIndex(addr, ":")
	if colonIdx < 0 { t.Fatalf("addr: %s", addr) }
	host, port := addr[:colonIdx], addr[colonIdx+1:]
	dsn := "host=" + host + " port=" + port + " user=postgres dbname=postgres sslmode=disable"

	// Setup connection (plain, no notice handler) — mimics isolation runner's setup
	setupDB, err := sql.Open("postgres", dsn)
	if err != nil { t.Fatal(err) }
	defer setupDB.Close()
	ctx := context.Background()
	setupConn, err := setupDB.Conn(ctx)
	if err != nil { t.Fatal(err) }
	for _, q := range []string{
		`CREATE TABLE sep_trig_test (key int primary key, balance int, status text, val text)`,
		`INSERT INTO sep_trig_test VALUES (1, 160, 's1', 'setup')`,
		`CREATE FUNCTION sep_trig_fn() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF tg_op = 'UPDATE' THEN RAISE NOTICE 'Update: % -> %', OLD, NEW; END IF; RETURN NEW; END $$`,
		`CREATE TRIGGER sep_trig BEFORE INSERT OR UPDATE OR DELETE ON sep_trig_test FOR EACH ROW EXECUTE FUNCTION sep_trig_fn()`,
	} {
		rows, err := setupConn.QueryContext(ctx, q)
		if err != nil { t.Fatalf("setup %q: %v", q, err) }
		rows.Close()
	}
	_ = setupConn.Close()

	// Session connection (with notice handler) — mimics s2 session in isolation test
	base, err := pq.NewConnector(dsn)
	if err != nil { t.Skipf("connector: %v", err) }
	var noticeMu sync.Mutex
	var notices []string
	withNotice := pq.ConnectorWithNoticeHandler(base, func(n *pq.Error) {
		noticeMu.Lock()
		notices = append(notices, n.Message)
		noticeMu.Unlock()
	})
	sessDB := sql.OpenDB(withNotice)
	defer sessDB.Close()
	sessConn, err := sessDB.Conn(ctx)
	if err != nil { t.Fatal(err) }
	defer sessConn.Close()

	// Session setup (BEGIN) — mimics isolation runner session setup
	rows2, err := sessConn.QueryContext(ctx, `BEGIN ISOLATION LEVEL READ COMMITTED`)
	if err != nil { t.Fatalf("BEGIN: %v", err) }
	rows2.Close()

	noticeMu.Lock(); notices = nil; noticeMu.Unlock()

	// Run the update step
	rows3, err := sessConn.QueryContext(ctx, `UPDATE sep_trig_test t SET balance = balance + 10, val = t.val || ' updated' WHERE t.key = 1`)
	if err != nil { t.Fatalf("UPDATE: %v", err) }
	rows3.Close()

	noticeMu.Lock(); got := append([]string(nil), notices...); noticeMu.Unlock()
	t.Logf("captured notices (separate connections): %v", got)
	if len(got) == 0 {
		t.Error("no notice — trigger not firing with separate setup/session connections")
	} else {
		t.Logf("SUCCESS: got %d notice(s)", len(got))
	}
}

// TestNoticeSeparateConnectionsIsolationMimic directly mimics what the isolation 
// runner does for the merge-match-recheck spec, second permutation.
func TestNoticeSeparateConnectionsIsolationMimic(t *testing.T) {
	addr, _, stop := startCopyExecServer(t)
	defer stop()

	colonIdx := strings.LastIndex(addr, ":")
	if colonIdx < 0 { t.Fatalf("addr: %s", addr) }
	host, port := addr[:colonIdx], addr[colonIdx+1:]
	dsn := "host=" + host + " port=" + port + " user=postgres dbname=postgres sslmode=disable"

	// === Global Setup (like the spec's global setup, on a plain DB) ===
	setupDB, _ := sql.Open("postgres", dsn)
	defer setupDB.Close()
	ctx := context.Background()
	setupConn, _ := setupDB.Conn(ctx)
	setupSQL := strings.Join([]string{
		`CREATE TABLE target_tg_mimic (key int primary key, balance integer, status text, val text)`,
		`INSERT INTO target_tg_mimic VALUES (1, 160, 's1', 'setup')`,
		`CREATE FUNCTION target_tg_mimic_fn() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
  IF tg_op = 'INSERT' THEN
    RAISE NOTICE 'Insert: %', NEW;
    RETURN NEW;
  ELSIF tg_op = 'UPDATE' THEN
    RAISE NOTICE 'Update: % -> %', OLD, NEW;
    RETURN NEW;
  ELSE
    RAISE NOTICE 'Delete: %', OLD;
    RETURN OLD;
  END IF;
END
$$`,
		`CREATE TRIGGER target_tg_mimic_trig BEFORE INSERT OR UPDATE OR DELETE ON target_tg_mimic FOR EACH ROW EXECUTE FUNCTION target_tg_mimic_fn()`,
	}, "; ")
	// Run each as separate queries (like execConn in isolation runner)
	for _, q := range []string{
		`CREATE TABLE target_tg_mimic (key int primary key, balance integer, status text, val text)`,
		`INSERT INTO target_tg_mimic VALUES (1, 160, 's1', 'setup')`,
		`CREATE FUNCTION target_tg_mimic_fn() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF tg_op = 'INSERT' THEN RAISE NOTICE 'Insert: %', NEW; RETURN NEW; ELSIF tg_op = 'UPDATE' THEN RAISE NOTICE 'Update: % -> %', OLD, NEW; RETURN NEW; ELSE RAISE NOTICE 'Delete: %', OLD; RETURN OLD; END IF; END $$`,
		`CREATE TRIGGER target_tg_mimic_trig BEFORE INSERT OR UPDATE OR DELETE ON target_tg_mimic FOR EACH ROW EXECUTE FUNCTION target_tg_mimic_fn()`,
	} {
		rows, err := setupConn.QueryContext(ctx, q)
		if err != nil { t.Fatalf("setup %q: %v", q[:min(50,len(q))], err) }
		rows.Close()
	}
	_ = setupSQL
	_ = setupConn.Close()
	_ = setupDB.Close()

	// === Session s2 with notice handler ===
	base, err := pq.NewConnector(dsn)
	if err != nil { t.Skipf("connector: %v", err) }
	var noticeMu sync.Mutex
	var notices []string
	withNotice := pq.ConnectorWithNoticeHandler(base, func(n *pq.Error) {
		noticeMu.Lock()
		notices = append(notices, n.Message)
		noticeMu.Unlock()
	})
	s2DB := sql.OpenDB(withNotice)
	defer s2DB.Close()
	s2Conn, _ := s2DB.Conn(ctx)
	defer s2Conn.Close()

	// Session setup: BEGIN
	r, _ := s2Conn.QueryContext(ctx, `BEGIN ISOLATION LEVEL READ COMMITTED`)
	r.Close()

	// Clear stale notices (like execStepFromQueue does)
	noticeMu.Lock(); notices = nil; noticeMu.Unlock()

	// Run update1_tg step
	rows, err := s2Conn.QueryContext(ctx, `UPDATE target_tg_mimic t SET balance = balance + 10, val = t.val || ' updated by update1_tg' WHERE t.key = 1`)
	if err != nil { t.Fatalf("UPDATE: %v", err) }
	rows.Close()

	noticeMu.Lock(); got := append([]string(nil), notices...); noticeMu.Unlock()
	t.Logf("notices from update1_tg mimic: %v", got)
	if len(got) == 0 {
		t.Error("FAIL: no notices — BEFORE UPDATE trigger not firing in isolation mimic")
	}
}

// TestNoticeSpecSetupSQL tests that the CREATE TRIGGER in spec setup SQL
// (sent as one multi-statement query) correctly registers the trigger.
func TestNoticeSpecSetupSQL(t *testing.T) {
	addr, _, stop := startCopyExecServer(t)
	defer stop()
	colonIdx := strings.LastIndex(addr, ":")
	if colonIdx < 0 { t.Fatalf("addr: %s", addr) }
	host, port := addr[:colonIdx], addr[colonIdx+1:]
	dsn := "host=" + host + " port=" + port + " user=postgres dbname=postgres sslmode=disable"

	base, _ := pq.NewConnector(dsn)
	var noticeMu sync.Mutex
	var notices []string
	withNotice := pq.ConnectorWithNoticeHandler(base, func(n *pq.Error) {
		noticeMu.Lock(); notices = append(notices, n.Message); noticeMu.Unlock()
	})
	db := sql.OpenDB(withNotice)
	defer db.Close()
	ctx := context.Background()
	
	// Run setup as one big multi-statement string (like execConn in isolation runner)
	setupSQL := `CREATE TABLE target_tg_spec (key int primary key, balance integer, status text, val text);
INSERT INTO target_tg_spec VALUES (1, 160, 's1', 'setup');
CREATE FUNCTION target_tg_spec_fn() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
  IF tg_op = 'INSERT' THEN
    RAISE NOTICE 'Insert: %', NEW;
    RETURN NEW;
  ELSIF tg_op = 'UPDATE' THEN
    RAISE NOTICE 'Update: % -> %', OLD, NEW;
    RETURN NEW;
  ELSE
    RAISE NOTICE 'Delete: %', OLD;
    RETURN OLD;
  END IF;
END
$$;
CREATE TRIGGER target_tg_spec_trig BEFORE INSERT OR UPDATE OR DELETE ON target_tg_spec
  FOR EACH ROW EXECUTE FUNCTION target_tg_spec_fn()`

	setupConn, _ := db.Conn(ctx)
	rows, err := setupConn.QueryContext(ctx, setupSQL)
	if err != nil { t.Fatalf("setup: %v", err) }
	rows.Close()
	_ = setupConn.Close()

	// Now test with explicit transaction + UPDATE
	sessConn, _ := db.Conn(ctx)
	defer sessConn.Close()
	r, _ := sessConn.QueryContext(ctx, `BEGIN ISOLATION LEVEL READ COMMITTED`); r.Close()

	noticeMu.Lock(); notices = nil; noticeMu.Unlock()
	rows2, err := sessConn.QueryContext(ctx, `UPDATE target_tg_spec t SET balance = balance + 10, val = t.val || ' updated' WHERE t.key = 1`)
	if err != nil { t.Fatalf("UPDATE: %v", err) }
	rows2.Close()

	noticeMu.Lock(); got := append([]string(nil), notices...); noticeMu.Unlock()
	t.Logf("notices (spec setup SQL as one string): %v", got)
	if len(got) == 0 {
		t.Error("FAIL: no notices — CREATE FUNCTION/TRIGGER in multi-stmt setup not working")
	}
}
