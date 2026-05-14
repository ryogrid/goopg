// Package pubsubcluster orchestrates a logical-replication pair —
// publisher + subscriber — where either side can be a goopg cluster
// or an upstream PostgreSQL cluster. Used by the M0103 heterogeneous
// failover E2E tests and by `TestPort_PgoutputInteropGoopgToPG`.
//
// The PubSubCluster wraps two `ReplPeer`s. ReplPeer is the minimal
// surface common to a goopg `*cluster.Cluster` and a PG
// `*pgcluster.Cluster`: dial info, lifecycle, and a single-shot
// exec/query. The harness owns the SQL it ships across the
// boundary (CREATE PUBLICATION/SUBSCRIPTION, applylsn polling); tests
// drive the workload directly through the peer accessors.
//
// See docs/design/0103-0005-heterogeneous-logical-failover-e2e-harness.md.
package pubsubcluster

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/goopg/goopg/internal/testutil/cluster"
	"github.com/goopg/goopg/internal/testutil/pgcluster"
)

// ClusterKind selects which binary backs a peer.
type ClusterKind int

const (
	ClusterKindGoopg ClusterKind = iota
	ClusterKindPG
)

func (k ClusterKind) String() string {
	switch k {
	case ClusterKindGoopg:
		return "goopg"
	case ClusterKindPG:
		return "pg"
	default:
		return fmt.Sprintf("unknown(%d)", int(k))
	}
}

// SyncMode chooses async vs synchronous_commit=remote_apply on the publisher.
type SyncMode int

const (
	SyncModeAsync SyncMode = iota
	SyncModeRemoteApply
)

func (m SyncMode) String() string {
	switch m {
	case SyncModeAsync:
		return "async"
	case SyncModeRemoteApply:
		return "sync_remote_apply"
	default:
		return fmt.Sprintf("unknown(%d)", int(m))
	}
}

// ReplPeer is the dial-and-drive surface common to both binaries.
// The harness uses it to spin up and tear down a cluster, ship a
// statement across the wire, read back a scalar, and to mint the
// conninfo line the other peer's apply worker dials.
type ReplPeer interface {
	Kind() ClusterKind
	Host() string
	Port() int
	User() string
	Database() string
	Conninfo(applicationName string) string
	Start() error
	Stop() error
	// Kill terminates the cluster's underlying postmaster (or goopg
	// server) with SIGKILL — the upstream equivalent of an unclean
	// crash. The peer is left in the "not running" state; subsequent
	// Stop() calls are no-ops. Used by M0103-0007 rung 22 to verify
	// libpq multi-host reconnect against a dead publisher.
	Kill() error
	Exec(t *testing.T, sql string)
	QueryScalar(t *testing.T, sql string) string
	// Pgbench runs the cluster-local pgbench binary with the supplied
	// args appended after the standard connection flags
	// (`-h/-p/-U <database>`). Returns combined stdout+stderr; fails
	// the test on non-zero exit. M0103-0007 rung 20 uses this to
	// drive a pgbench-shape workload from either side of the pair.
	Pgbench(t *testing.T, args ...string) string
}

// Options configures the pair.
type Options struct {
	RepoRoot         string
	BaseDir          string
	PublisherKind    ClusterKind
	SubscriberKind   ClusterKind
	SyncMode         SyncMode
	ApplicationName  string
	PublicationName  string
	SubscriptionName string
	StartupWait      time.Duration
	ShutdownWait     time.Duration
}

// PubSubCluster is the publisher + subscriber pair handle.
type PubSubCluster struct {
	Publisher  ReplPeer
	Subscriber ReplPeer
	name       string
	opts       Options
}

// NewMixed constructs a pair of peers per opts.PublisherKind /
// SubscriberKind. Callers invoke `Start(ctx)`, then `CreatePublication`
// and `CreateSubscription`. Cleanup with `defer psc.Close()`.
//
// NewMixed itself skips the test via t.Skip when either side requires
// the upstream PG bin tree and that tree is missing — keeps `go test
// ./...` working on machines without `make local-install`.
func NewMixed(t *testing.T, name string, opts Options) *PubSubCluster {
	t.Helper()
	if strings.TrimSpace(name) == "" {
		t.Fatal("pubsubcluster: name is required")
	}
	if strings.TrimSpace(opts.RepoRoot) == "" {
		t.Fatal("pubsubcluster: RepoRoot is required")
	}
	baseDir := strings.TrimSpace(opts.BaseDir)
	if baseDir == "" {
		baseDir = filepath.Join(opts.RepoRoot, "tmp", "pubsubclusters", name)
	}
	if opts.PublicationName == "" {
		opts.PublicationName = "p"
	}
	if opts.SubscriptionName == "" {
		opts.SubscriptionName = "s"
	}
	if opts.ApplicationName == "" {
		opts.ApplicationName = opts.SubscriptionName
	}

	// If either side is PG, pre-check the bin tree once so the test
	// skips cleanly before any tempdir is allocated.
	if opts.PublisherKind == ClusterKindPG || opts.SubscriberKind == ClusterKindPG {
		pgcluster.Available(t, filepath.Join(opts.RepoRoot, "postgres", "local_install", "bin"))
	}

	psc := &PubSubCluster{name: name, opts: opts}

	// Publisher: must allow logical decoding + the
	// synchronous_standby_names rule when the sync subtest runs.
	pubExtra := []string{}
	if opts.SyncMode == SyncModeRemoteApply {
		pubExtra = append(pubExtra,
			fmt.Sprintf("synchronous_standby_names = '%s'", opts.ApplicationName),
			"synchronous_commit = remote_apply",
		)
	}
	pub, err := newPeer(t, name+"-pub", baseDir, opts.PublisherKind, opts, pubExtra, true /*publisher*/)
	if err != nil {
		t.Fatalf("pubsubcluster: publisher: %v", err)
	}
	psc.Publisher = pub

	sub, err := newPeer(t, name+"-sub", baseDir, opts.SubscriberKind, opts, nil, false /*publisher*/)
	if err != nil {
		_ = pub.Stop()
		t.Fatalf("pubsubcluster: subscriber: %v", err)
	}
	psc.Subscriber = sub

	return psc
}

func newPeer(t *testing.T, peerName, baseDir string, kind ClusterKind, opts Options, extraConf []string, publisher bool) (ReplPeer, error) {
	t.Helper()
	dataDir := filepath.Join(baseDir, peerName)
	switch kind {
	case ClusterKindGoopg:
		c, err := cluster.New(peerName, cluster.Options{
			RepoRoot:     opts.RepoRoot,
			DataDir:      dataDir,
			StartupWait:  opts.StartupWait,
			ShutdownWait: opts.ShutdownWait,
		})
		if err != nil {
			return nil, err
		}
		if err := c.Init(); err != nil {
			return nil, fmt.Errorf("init: %w", err)
		}
		// goopg's WAL is always logical-eligible; the only conf the
		// harness needs to inject is the sync rule on the publisher
		// when SyncModeRemoteApply is in play.
		for _, line := range extraConf {
			if err := c.AppendPostgresqlConf(line); err != nil {
				return nil, fmt.Errorf("append conf %q: %w", line, err)
			}
		}
		return &goopgPeer{c: c}, nil
	case ClusterKindPG:
		// Force the PG bootstrap superuser to "postgres" so its role
		// name matches goopg's hardcoded `postgres` — the apply
		// launcher's `parseSubscriptionConninfo` ignores the `user=`
		// keyword in the subscriber's conninfo and reuses the
		// subscriber server's own `cfg.User`, which on goopg is always
		// "postgres". Heterogeneous setups need both sides to share
		// the same role name for the dial to succeed.
		c, err := pgcluster.New(peerName, pgcluster.Options{
			RepoRoot:    opts.RepoRoot,
			DataDir:     dataDir,
			User:        "postgres",
			WalLevel:    "logical",
			ExtraConf:   extraConf,
			StartupWait: opts.StartupWait,
		})
		if err != nil {
			return nil, err
		}
		return &pgPeer{c: c}, nil
	default:
		return nil, fmt.Errorf("unknown ClusterKind %v", kind)
	}
}

// Start launches both peers. Order: publisher first (subscriber's apply
// worker will dial the publisher on startup once the subscription is
// created).
func (p *PubSubCluster) Start(ctx context.Context) error {
	_ = ctx
	if err := p.Publisher.Start(); err != nil {
		return fmt.Errorf("publisher start: %w", err)
	}
	if err := p.Subscriber.Start(); err != nil {
		_ = p.Publisher.Stop()
		return fmt.Errorf("subscriber start: %w", err)
	}
	return nil
}

// CreatePublication runs `CREATE PUBLICATION <opts.PublicationName>
// FOR TABLE <tables>` on the publisher.
func (p *PubSubCluster) CreatePublication(t *testing.T, tables ...string) {
	t.Helper()
	if len(tables) == 0 {
		t.Fatal("pubsubcluster: CreatePublication: at least one table required")
	}
	sql := fmt.Sprintf("CREATE PUBLICATION %s FOR TABLE %s",
		p.opts.PublicationName, strings.Join(tables, ", "))
	p.Publisher.Exec(t, sql)
}

// CreateSubscription runs the subscriber-side `CREATE SUBSCRIPTION`.
// The conninfo is derived from the publisher's bind, with
// `application_name=opts.ApplicationName` appended so SyncRep matches.
func (p *PubSubCluster) CreateSubscription(t *testing.T) {
	t.Helper()
	conn := p.Publisher.Conninfo(p.opts.ApplicationName)
	sql := fmt.Sprintf(
		"CREATE SUBSCRIPTION %s CONNECTION '%s' PUBLICATION %s WITH (enabled = true, copy_data = false)",
		p.opts.SubscriptionName, conn, p.opts.PublicationName)
	p.Subscriber.Exec(t, sql)
}

// MultiHostConninfo returns a libpq-style conninfo string listing the
// publisher and subscriber hosts/ports comma-separated. libpq tries each
// in order on connect, so after `Publisher.Kill()` a client invoked
// with this conninfo will fall through the dead publisher and land on
// the (always-writable) subscriber. Mirrors PG18's documented
// "multi-host conninfo" shape from §32.1.1 of libpq-connect.
//
// Used by M0103-0007 rung 22 to verify libpq multi-host reconnect
// against a SIGKILLed publisher.
func (p *PubSubCluster) MultiHostConninfo(applicationName string) string {
	conn := fmt.Sprintf("host=%s,%s port=%d,%d user=%s dbname=%s",
		p.Publisher.Host(), p.Subscriber.Host(),
		p.Publisher.Port(), p.Subscriber.Port(),
		p.Publisher.User(), p.Publisher.Database())
	if strings.TrimSpace(applicationName) != "" {
		conn += " application_name=" + applicationName
	}
	return conn
}

// WaitForRow polls the subscriber until `SELECT count(*) FROM table
// WHERE pred` returns `want` or the deadline fires.
func (p *PubSubCluster) WaitForRow(t *testing.T, table, pred string, want int, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	q := fmt.Sprintf("SELECT count(*) FROM %s WHERE %s", table, pred)
	for time.Now().Before(deadline) {
		got := p.Subscriber.QueryScalar(t, q)
		if got == fmt.Sprintf("%d", want) {
			return
		}
		time.Sleep(200 * time.Millisecond)
	}
	t.Fatalf("pubsubcluster: WaitForRow %s: did not see count=%d within %s",
		q, want, timeout)
}

// Close tears down both peers. Errors are joined so a transient
// subscriber-side failure doesn't hide a publisher-side one.
func (p *PubSubCluster) Close() error {
	var errs []string
	if p.Subscriber != nil {
		if err := p.Subscriber.Stop(); err != nil {
			errs = append(errs, "subscriber: "+err.Error())
		}
	}
	if p.Publisher != nil {
		if err := p.Publisher.Stop(); err != nil {
			errs = append(errs, "publisher: "+err.Error())
		}
	}
	if len(errs) > 0 {
		return errors.New("pubsubcluster: close: " + strings.Join(errs, "; "))
	}
	return nil
}
