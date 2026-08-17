package pubsubcluster

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestPubSubClusterSmokePGToGoopg is the harness smoke test mandated
// by M0103-0006: spin up both binaries (PG publisher + goopg
// subscriber), drive an INSERT on the publisher, observe the row on
// the subscriber within a deadline.
//
// Skips when the upstream PG bin tree isn't present (`make
// local-install` not run yet).
//
// goopg's `CREATE SUBSCRIPTION` doesn't yet honour the upstream
// `WITH (create_slot = true)` default — it never dials the publisher
// to issue CREATE_REPLICATION_SLOT. This is tracked as a known
// follow-up in `.ralph/fix_plan.md` (M0103-0007 dependency). The
// smoke harness pre-creates the slot via the PG-side
// `pg_create_logical_replication_slot` SQL function so the apply
// worker finds it on first dial.
func TestPubSubClusterSmokePGToGoopg(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping smoke test in short mode")
	}
	repo := repoRoot(t)
	baseDir := filepath.Join(repo, "tmp", "pubsubcluster-smoke-pg2g")
	_ = os.RemoveAll(baseDir)

	slotName := "smoke_sub"
	psc := NewMixed(t, "smoke_pg2g", Options{
		RepoRoot:         repo,
		BaseDir:          baseDir,
		PublisherKind:    ClusterKindPG,
		SubscriberKind:   ClusterKindGoopg,
		SyncMode:         SyncModeAsync,
		ApplicationName:  "smoke_sub",
		PublicationName:  "p",
		SubscriptionName: slotName,
		StartupWait:      30 * time.Second,
		ShutdownWait:     10 * time.Second,
	})
	defer func() {
		_ = psc.Close()
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	if err := psc.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}

	// Schema on both ends so the subscriber's apply worker has the
	// relation it needs. PG names the relation `public.t`; goopg's
	// catalog matches by (schema, name) exactly, so the subscriber's
	// CREATE TABLE must qualify the schema to land under `public`.
	psc.Publisher.Exec(t, "CREATE TABLE public.t (id int PRIMARY KEY, v text)")
	psc.Subscriber.Exec(t, "CREATE TABLE public.t (id int PRIMARY KEY, v text)")

	psc.CreatePublication(t, "t")

	// Pre-create the logical slot on the PG publisher so the goopg
	// subscriber's CREATE SUBSCRIPTION (which doesn't auto-create
	// slots yet) finds an existing slot on first dial. The slot's
	// output plugin must be pgoutput because that's what the
	// publisher-side walsender ships and what the goopg apply worker
	// understands.
	psc.Publisher.Exec(t, fmt.Sprintf(
		"SELECT pg_create_logical_replication_slot('%s', 'pgoutput')", slotName))

	// CREATE SUBSCRIPTION with slot_name pointing at the pre-created
	// slot + create_slot=false so the goopg apply worker doesn't try
	// to create it.
	conn := psc.Publisher.Conninfo(psc.opts.ApplicationName)
	psc.Subscriber.Exec(t, fmt.Sprintf(
		"CREATE SUBSCRIPTION %s CONNECTION '%s' PUBLICATION %s WITH (enabled = true, copy_data = false, slot_name = '%s', create_slot = false)",
		psc.opts.SubscriptionName, conn, psc.opts.PublicationName, slotName))

	psc.Publisher.Exec(t, "INSERT INTO public.t VALUES (1, 'hello')")

	// Wait for the apply worker on the subscriber to consume at
	// least one commit from the publisher. This is the harness's
	// minimum-viable evidence that both binaries handshook,
	// publication/subscription was issued correctly, and the apply
	// path is live.
	waitForApplyCommitLog(t,
		filepath.Join(repo, "tmp", "pubsubcluster-smoke-pg2g", "smoke_pg2g-sub", "cluster.log"),
		30*time.Second)
}

// TestPubSubClusterSmokePGToGoopgFreshSessionVisibility is the
// M0103-0007 rung-1 pin: a row applied by the subscriber's apply
// worker must be visible to a fresh `database/sql` SELECT against
// the subscriber. The M0103-0006 caveat noted the disk file under
// `base/1/<oid>` carried the tuple but `SELECT count(*)` from a new
// session returned 0; closing M0103-0007's Scenario A requires that
// invariant to hold.
func TestPubSubClusterSmokePGToGoopgFreshSessionVisibility(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping fresh-session-visibility smoke test in short mode")
	}
	repo := repoRoot(t)
	baseDir := filepath.Join(repo, "tmp", "pubsubcluster-vis-pg2g")
	_ = os.RemoveAll(baseDir)

	slotName := "vis_sub"
	psc := NewMixed(t, "vis_pg2g", Options{
		RepoRoot:         repo,
		BaseDir:          baseDir,
		PublisherKind:    ClusterKindPG,
		SubscriberKind:   ClusterKindGoopg,
		SyncMode:         SyncModeAsync,
		ApplicationName:  "vis_sub",
		PublicationName:  "p",
		SubscriptionName: slotName,
		StartupWait:      30 * time.Second,
		ShutdownWait:     10 * time.Second,
	})
	defer func() { _ = psc.Close() }()

	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	if err := psc.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}

	psc.Publisher.Exec(t, "CREATE TABLE public.t (id int PRIMARY KEY, v text)")
	psc.Subscriber.Exec(t, "CREATE TABLE public.t (id int PRIMARY KEY, v text)")
	psc.CreatePublication(t, "t")
	psc.Publisher.Exec(t, fmt.Sprintf(
		"SELECT pg_create_logical_replication_slot('%s', 'pgoutput')", slotName))
	conn := psc.Publisher.Conninfo(psc.opts.ApplicationName)
	psc.Subscriber.Exec(t, fmt.Sprintf(
		"CREATE SUBSCRIPTION %s CONNECTION '%s' PUBLICATION %s WITH (enabled = true, copy_data = false, slot_name = '%s', create_slot = false)",
		psc.opts.SubscriptionName, conn, psc.opts.PublicationName, slotName))

	psc.Publisher.Exec(t, "INSERT INTO public.t VALUES (1, 'hello')")
	waitForApplyCommitLog(t,
		filepath.Join(repo, "tmp", "pubsubcluster-vis-pg2g", "vis_pg2g-sub", "cluster.log"),
		30*time.Second)

	// Fresh `database/sql` session — Cluster.Query opens a new
	// connection per call. The equality predicate on the PK column
	// `id = 1` exercises the IndexScan path: before the rung-1 fix
	// (`maintainUniqueIndexesForInsert` in applyInsert) the row was
	// only reachable via SeqScan, so `WHERE id = 1` returned 0
	// rows even though `SELECT id FROM public.t` showed it as 1.
	psc.WaitForRow(t, "public.t", "id = 1", 1, 10*time.Second)
}

// waitForApplyCommitLog polls a goopg cluster log file until at least
// one "logical apply: commit" line is observed. That structured-log
// line is the apply worker's per-commit beacon; its presence proves
// the subscriber-side apply pipeline received and processed a
// transaction from the publisher.
func waitForApplyCommitLog(t *testing.T, logPath string, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		data, err := os.ReadFile(logPath)
		if err == nil && bytesContainsAll(data, "logical apply: commit") {
			return
		}
		time.Sleep(200 * time.Millisecond)
	}
	t.Fatalf("pubsubcluster: log %s did not show an apply commit within %s", logPath, timeout)
}

func bytesContainsAll(b []byte, s string) bool {
	return len(b) > 0 && string(b) != "" && (len(s) == 0 || indexBytes(b, s) >= 0)
}

func indexBytes(b []byte, s string) int {
	if len(s) == 0 || len(b) < len(s) {
		return -1
	}
	for i := 0; i+len(s) <= len(b); i++ {
		if string(b[i:i+len(s)]) == s {
			return i
		}
	}
	return -1
}

// fakeScalarPeer is a minimal ReplPeer stub used to unit-test
// WaitForScalar without spinning up a real publisher/subscriber pair.
// Only QueryScalar is exercised; every other method is an unused stub.
type fakeScalarPeer struct {
	values []string // successive QueryScalar results, last one repeats once exhausted
	calls  int
}

func (f *fakeScalarPeer) Kind() ClusterKind { return ClusterKindGoopg }
func (f *fakeScalarPeer) Host() string      { return "" }
func (f *fakeScalarPeer) Port() int         { return 0 }
func (f *fakeScalarPeer) User() string      { return "" }
func (f *fakeScalarPeer) Database() string  { return "" }

func (f *fakeScalarPeer) Conninfo(applicationName string) string { return "" }

func (f *fakeScalarPeer) Start() error { return nil }
func (f *fakeScalarPeer) Stop() error  { return nil }
func (f *fakeScalarPeer) Kill() error  { return nil }

func (f *fakeScalarPeer) Exec(t *testing.T, sql string) {}

func (f *fakeScalarPeer) Pgbench(t *testing.T, args ...string) string { return "" }

func (f *fakeScalarPeer) QueryScalar(t *testing.T, sql string) string {
	idx := f.calls
	if idx >= len(f.values) {
		idx = len(f.values) - 1
	}
	f.calls++
	return f.values[idx]
}

// TestWaitForScalarSuccess pins the success path: WaitForScalar must
// return once the polled value matches `want`, without waiting out the
// full timeout. This is the shape the bpchar-padding regression needed
// — a value change (UPDATE) that WaitForRow's count(*) polling cannot
// observe.
func TestWaitForScalarSuccess(t *testing.T) {
	peer := &fakeScalarPeer{values: []string{"2", "2", "3"}}
	psc := &PubSubCluster{Subscriber: peer}
	psc.WaitForScalar(t, "SELECT length(filler) FROM t WHERE id = 1", "3", 5*time.Second)
	if peer.calls < 3 {
		t.Fatalf("expected WaitForScalar to poll until the value changed, got %d calls", peer.calls)
	}
}

func repoRoot(t *testing.T) string {
	t.Helper()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	cur := wd
	for {
		if _, err := os.Stat(filepath.Join(cur, "go.mod")); err == nil {
			return cur
		}
		next := filepath.Dir(cur)
		if next == cur {
			t.Fatalf("could not locate go.mod from %s", wd)
		}
		cur = next
	}
}
