package testport

import (
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/goopg/goopg/internal/testutil/tapnode"
)

// TestPort_Recovery029StatsRestart ports
// postgres/src/test/recovery/t/029_stats_restart.pl (M0149-0007): the
// cumulative statistics system persists its stats to pg_stat/pgstat.stat at
// a clean shutdown and restores them at startup; an immediate shutdown (crash)
// discards them, as does an invalid stats file; checkpointer and WAL stats
// keep their counters and stats_reset across restarts, and
// pg_stat_reset_shared resets one kind without touching the other.
func TestPort_Recovery029StatsRestart(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping recovery TAP port in short mode")
	}
	repo := repoRoot(t)
	tapnode.Available(t, repo)

	node := tapnode.NewGoopg(t, repo, "primary")
	node.AppendConf("wal_level = replica")
	node.AppendConf("max_wal_senders = 10")
	node.AppendConf("track_functions = 'all'")
	node.Start()

	const connectDB = "postgres"
	const dbUnderTest = "test"

	q := func(db, sql string) string { return node.SafePSQL(db, sql) }
	num := func(s string) int64 {
		v, err := strconv.ParseInt(s, 10, 64)
		if err != nil {
			t.Errorf("not ok: %q is not an integer", s)
		}
		return v
	}
	is := func(got, want, name string) {
		t.Helper()
		if got != want {
			t.Errorf("not ok: %s: got %q, want %q", name, got, want)
		}
	}
	ok := func(cond bool, name string) {
		t.Helper()
		if !cond {
			t.Errorf("not ok: %s", name)
		}
	}

	ioStats := func(context, object, backendType string) (writes, reads string) {
		where := " FROM pg_stat_io WHERE context = '" + context + "' AND object = '" + object +
			"' AND backend_type = '" + backendType + "'"
		return q(connectDB, "SELECT writes"+where), q(connectDB, "SELECT reads"+where)
	}
	type ckpt struct{ count, reset string }
	type walst struct{ records, bytes, reset string }
	checkpointStats := func() ckpt {
		return ckpt{
			count: q(connectDB, "SELECT num_timed + num_requested FROM pg_stat_checkpointer"),
			reset: q(connectDB, "SELECT stats_reset FROM pg_stat_checkpointer"),
		}
	}
	walStats := func() walst {
		return walst{
			records: q(connectDB, "SELECT wal_records FROM pg_stat_wal"),
			bytes:   q(connectDB, "SELECT wal_bytes FROM pg_stat_wal"),
			reset:   q(connectDB, "SELECT stats_reset FROM pg_stat_wal"),
		}
	}
	triggerFuncrelStat := func() {
		q(dbUnderTest, `
SELECT * FROM tab_stats_crash_discard_test1;
SELECT func_stats_crash_discard1();
SELECT pg_stat_force_next_flush();`)
	}
	haveStats := func(kind, dboid, objid string) string {
		return q(connectDB, "SELECT pg_stat_have_stats('"+kind+"', "+dboid+", "+objid+")")
	}

	// Verify that IO stats are captured during startup.
	sect := "startup"
	saWrites, _ := ioStats("init", "wal", "standalone backend")
	_, stReads := ioStats("normal", "wal", "startup")
	ok(num(saWrites) > 0, sect+": increased standalone backend IO writes")
	ok(num(stReads) > 0, sect+": increased startup IO reads")

	// create test objects
	q(connectDB, "CREATE DATABASE "+dbUnderTest)
	q(dbUnderTest, "CREATE TABLE tab_stats_crash_discard_test1 AS SELECT generate_series(1,100) AS a")
	q(dbUnderTest, "CREATE FUNCTION func_stats_crash_discard1() RETURNS VOID AS 'select 2;' LANGUAGE SQL IMMUTABLE")

	// collect object oids
	dboid := q(dbUnderTest, "SELECT oid FROM pg_database WHERE datname = '"+dbUnderTest+"'")
	funcoid := q(dbUnderTest, "SELECT 'func_stats_crash_discard1()'::regprocedure::oid")
	tableoid := q(dbUnderTest, "SELECT 'tab_stats_crash_discard_test1'::regclass::oid")

	// generate stats and flush them
	triggerFuncrelStat()

	checkAll := func(sect, want string) {
		t.Helper()
		is(haveStats("database", dboid, "0"), want, sect+": db stats")
		is(haveStats("function", dboid, funcoid), want, sect+": function stats")
		is(haveStats("relation", dboid, tableoid), want, sect+": relation stats")
	}

	// verify stats objects exist
	checkAll("initial", "t")

	// regular shutdown
	node.Stop(tapnode.StopFast)

	// backup stats files
	statsfile := filepath.Join(t.TempDir(), "discard_stats1")
	_, err := os.Stat(statsfile)
	ok(os.IsNotExist(err), "backup statsfile cannot already exist")

	ogStats := filepath.Join(node.DataDir(), "pg_stat", "pgstat.stat")
	_, err = os.Stat(ogStats)
	ok(err == nil, "origin stats file must exist")
	if data, rerr := os.ReadFile(ogStats); rerr == nil {
		if werr := os.WriteFile(statsfile, data, 0o600); werr != nil {
			t.Fatal(werr)
		}
	} else {
		t.Fatalf("Copy failed: %v", rerr)
	}

	// test discarding of stats file after crash etc
	node.Start()
	checkAll("copy", "t")

	node.Stop(tapnode.StopImmediate)
	_, err = os.Stat(ogStats)
	ok(os.IsNotExist(err), "no stats file should exist after immediate shutdown")

	// copy the old stats back to test we discard stats after crash restart
	if data, rerr := os.ReadFile(statsfile); rerr == nil {
		if werr := os.WriteFile(ogStats, data, 0o600); werr != nil {
			t.Fatal(werr)
		}
	}
	node.Start()

	// stats should have been discarded
	checkAll("post immediate", "f")

	// get rid of backup statsfile
	_ = os.Remove(statsfile)

	// generate new stats and flush them
	triggerFuncrelStat()
	checkAll("post immediate, new", "t")

	// test discarding of stats file after invalid data
	node.Stop(tapnode.StopFast)
	if err := os.WriteFile(ogStats, []byte("ZZZZZZZZZZZZZ"), 0o600); err != nil {
		t.Fatal(err)
	}
	node.Start()
	checkAll("invalid_overwrite", "f")

	// test discarding of stats file after invalid data (trailing bytes)
	triggerFuncrelStat()
	node.Stop(tapnode.StopFast)
	f, err := os.OpenFile(ogStats, os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatalf("could not append to %q: %v", ogStats, err)
	}
	_, _ = f.WriteString("XYZ")
	_ = f.Close()
	node.Start()
	checkAll("invalid_append", "f")

	// Test that checkpoints and WAL stats are persisted across restarts.
	q(connectDB, "CHECKPOINT; CHECKPOINT;")
	ckptStart := checkpointStats()
	walStart := walStats()
	restart := func() {
		node.Stop(tapnode.StopFast)
		node.Start()
	}
	restart()

	sect = "post restart"
	ckptRestart := checkpointStats()
	walRestart := walStats()
	ok(num(ckptStart.count) < num(ckptRestart.count), sect+": increased checkpoint count")
	ok(num(walStart.records) < num(walRestart.records), sect+": increased wal record count")
	ok(num(walStart.bytes) < num(walRestart.bytes), sect+": increased wal bytes")
	is(ckptRestart.reset, ckptStart.reset, sect+": checkpoint stats_reset equal")
	is(walRestart.reset, walStart.reset, sect+": wal stats_reset equal")

	// Test that checkpoint stats can be reset without affecting WAL stats.
	q(connectDB, "SELECT pg_stat_reset_shared('checkpointer')")
	sect = "post ckpt reset"
	ckptReset := checkpointStats()
	walCkptReset := walStats()
	ok(num(ckptRestart.count) > num(ckptReset.count), sect+": checkpoint count smaller")
	ok(ckptStart.reset < ckptReset.reset, sect+": stats_reset newer")
	ok(num(walRestart.records) <= num(walCkptReset.records), sect+": wal record count not affected by reset")
	is(walCkptReset.reset, walStart.reset, sect+": wal stats_reset equal")

	// Test that checkpoint stats reset is persisted across restarts.
	restart()
	sect = "post ckpt reset & restart"
	ckptRestartReset := checkpointStats()
	walRestart2 := walStats()
	// made sure above there's enough checkpoints that this will be stable even on slow machines
	ok(num(ckptRestartReset.count) < num(ckptRestart.count), sect+": checkpoint still reset")
	is(ckptRestartReset.reset, ckptReset.reset, sect+": stats_reset same")
	ok(num(walCkptReset.records) < num(walRestart2.records), sect+": increased wal record count")
	ok(num(walCkptReset.bytes) < num(walRestart2.bytes), sect+": increased wal bytes")
	is(walRestart2.reset, walStart.reset, sect+": wal stats_reset equal")

	// Test that WAL stats can be reset.
	q(connectDB, "SELECT pg_stat_reset_shared('wal')")
	sect = "post wal reset"
	walReset := walStats()
	ok(num(walReset.records) < num(walRestart2.records), sect+": smaller record count")
	ok(num(walReset.bytes) < num(walRestart2.bytes), sect+": smaller bytes")
	ok(walReset.reset > walRestart2.reset, sect+": newer stats_reset")

	// Test that WAL stats reset is persisted across restarts.
	restart()
	sect = "post wal reset & restart"
	walResetRestart := walStats()
	// enough WAL generated to make test stable
	ok(num(walResetRestart.records) < num(walRestart2.records), sect+": smaller record count")
	ok(num(walReset.bytes) < num(walRestart2.bytes), sect+": smaller bytes")
	ok(walReset.reset > walRestart2.reset, sect+": newer stats_reset")

	// Test that WAL stats are reset after a crash restart.
	node.Stop(tapnode.StopImmediate)
	node.Start()
	sect = "post immediate restart"
	walRestartImmediate := walStats()
	ok(walResetRestart.reset < walRestartImmediate.reset, sect+": reset timestamp is new")

	node.Stop(tapnode.StopFast)
}
