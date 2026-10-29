package testport

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/goopg/goopg/internal/testutil/cluster"
	"github.com/goopg/goopg/internal/testutil/pgcluster"
)

// TestE2E_GoopgStandbyFollowsPGTimelineSwitch is the receiver-side oracle for
// timeline following, PG's 004_timeline_switch shape with a goopg
// downstream:
//
//  1. PG 18.3 primary A streams to PG standby B and goopg standby C, both
//     pg_basebackup'd from A on timeline 1.
//  2. C stops, then A stops (fast: B receives its shutdown checkpoint) and B
//     is promoted onto timeline 2.
//  3. C restarts pointed at B. Its walreceiver fetches 00000002.history and
//     asks for timeline 1. B's walsender streams the rest of timeline 1 and
//     ends it at its switch point (CopyDone, then the next_tli row). C's standby moves its WAL
//     writer onto timeline 2 (recovery_target_timeline = 'latest', the
//     default) and streams it.
//
// A row written on B after the promotion reaching C, with C's WAL writer on
// timeline 2, shows the goopg walreceiver speaks the end-of-timeline protocol
// as PG's walsender expects it.
func TestE2E_GoopgStandbyFollowsPGTimelineSwitch(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping replication test in short mode")
	}
	repo := repoRoot(t)
	pgcluster.Available(t, filepath.Join(repo, "postgres", "local_install", "bin"))
	pgBasebackupBin := clientToolBin(t, "pg_basebackup")
	if pgBasebackupBin == "" {
		t.Skip("pg_basebackup not found")
	}
	baseDir := t.TempDir()

	a, err := pgcluster.New("tlf-pg-a", pgcluster.Options{
		RepoRoot:    repo,
		DataDir:     filepath.Join(baseDir, "pg-a"),
		User:        "postgres",
		WalLevel:    "replica",
		StartupWait: 30 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = a.Stop() }()
	if err := a.Start(); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := a.WaitReady(ctx, 15*time.Second); err != nil {
		t.Fatal(err)
	}
	a.Exec(t, "CREATE TABLE public.tlf_t (id int)")
	a.Exec(t, "INSERT INTO public.tlf_t VALUES (1)")
	a.Exec(t, "SELECT pg_create_physical_replication_slot('pg_b')")
	a.Exec(t, "SELECT pg_create_physical_replication_slot('goopg_c')")

	// B: a PG standby of A.
	dirB := filepath.Join(baseDir, "pg-b")
	runPGBasebackup(t, repo, pgBasebackupBin, a, dirB, "pg_b")
	configurePGStandbyFromGoopgBackup(t, dirB, a.Conninfo("pg_b"), "pg_b")
	b, err := pgcluster.OpenExisting("tlf-pg-b", pgcluster.Options{RepoRoot: repo, DataDir: dirB, User: "postgres", StartupWait: 45 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = b.Stop() }()
	if err := b.Start(); err != nil {
		t.Fatalf("B start: %v", err)
	}

	// C: a goopg standby of A.
	dirC := filepath.Join(baseDir, "goopg-c")
	runPGBasebackup(t, repo, pgBasebackupBin, a, dirC, "goopg_c")
	normalizePGWALSegmentNames(t, dirC)
	configureGoopgStandbyFromPGBackup(t, dirC, a.Conninfo("goopg_c"), "goopg_c")
	c, err := cluster.New("tlf-goopg-c", cluster.Options{
		RepoRoot:     repo,
		DataDir:      dirC,
		StartupWait:  45 * time.Second,
		ShutdownWait: 10 * time.Second,
		SyncInit:     true,
		SyncRuntime:  true,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = c.Stop(cluster.ShutdownImmediate) }()
	if err := c.Start(); err != nil {
		t.Fatalf("C start: %v", err)
	}

	a.Exec(t, "INSERT INTO public.tlf_t VALUES (2)")
	waitForGoopgCount(t, c, "SELECT count(*) FROM public.tlf_t WHERE id = 2", 1, 30*time.Second)
	waitForPGCount(t, b, "SELECT count(*) FROM public.tlf_t WHERE id = 2", 1, 30*time.Second)

	// C stops first, so the rest of timeline 1 (A's shutdown checkpoint)
	// reaches it from B after the restart; A stops; B is promoted onto
	// timeline 2. (Replaying A's shutdown checkpoint and then restarting
	// loses earlier rows on a goopg standby — P0-E23, unrelated to the
	// timeline switch.)
	if err := c.Stop(cluster.ShutdownFast); err != nil {
		t.Fatalf("C stop: %v", err)
	}
	if err := a.Stop(); err != nil {
		t.Fatalf("A stop: %v", err)
	}
	if err := b.Promote(); err != nil {
		t.Fatalf("B promote: %v", err)
	}
	b.Exec(t, "INSERT INTO public.tlf_t VALUES (3)")
	b.Exec(t, "SELECT pg_create_physical_replication_slot('goopg_c')")

	// C follows B across the switch.
	configureGoopgStandbyFromPGBackup(t, dirC, b.Conninfo("goopg_c"), "goopg_c")
	if err := c.Start(); err != nil {
		t.Fatalf("C restart: %v", err)
	}
	waitForGoopgCount(t, c, "SELECT count(*) FROM public.tlf_t WHERE id = 3", 1, 60*time.Second)
	if got := goopgCount(t, c, "SELECT count(*) FROM public.tlf_t"); got != 3 {
		rows, qerr := c.Query(context.Background(), "SELECT id FROM public.tlf_t ORDER BY id")
		t.Errorf("C rows = %d, want 3: %v %v", got, rows, qerr)
		if log, err := os.ReadFile(c.LogPath()); err == nil {
			t.Logf("C log:\n%s", log)
		}
	}
	if _, err := os.Stat(filepath.Join(dirC, "pg_wal", "00000002.history")); err != nil {
		t.Errorf("C lacks the fetched history file: %v", err)
	}
	segs, _ := filepath.Glob(filepath.Join(dirC, "pg_wal", "00000002000000000000000*"))
	if len(segs) == 0 {
		t.Errorf("C wrote no timeline-2 segment (pg_wal: %v)", listDir(filepath.Join(dirC, "pg_wal")))
	}
}

func listDir(dir string) []string {
	entries, _ := os.ReadDir(dir)
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	return names
}
