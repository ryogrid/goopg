// ApplyLauncher periodically scans the pg_subscription catalog and
// keeps one per-subscription apply worker running for every enabled
// row. Mirrors upstream's logical-replication launcher (see
// postgres/src/backend/replication/logical/launcher.c::ApplyLauncherMain)
// well enough that `CREATE SUBSCRIPTION ... WITH (enabled = true)`
// brings up an apply worker without operator intervention.
//
// Design: docs/design/0103-0001-apply-worker-launcher.md.

package server

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/executor"
	"github.com/goopg/goopg/internal/mvcc"
	"github.com/goopg/goopg/internal/storage"
	"github.com/goopg/goopg/internal/wal"
)

// ApplyLauncherConfig parameterises one launcher instance.
type ApplyLauncherConfig struct {
	PubSub  *catalog.PubSub
	Catalog catalog.Catalog
	Pool    *storage.Pool
	TxnMgr  *mvcc.Manager
	Slots   *wal.Slots
	Logger  *slog.Logger

	// PollInterval is how often the launcher rescans the catalog when
	// no Wake arrives. Zero falls back to defaultLauncherPoll.
	PollInterval time.Duration

	// LaunchFn is invoked in a freshly-spawned goroutine for every
	// enabled subscription that doesn't currently have a worker.
	// It runs until the per-worker ctx is cancelled or the receiver
	// loop returns. Tests inject a fake; production uses
	// DefaultLaunchApplyWorker.
	LaunchFn LaunchApplyWorkerFunc

	// User overrides the role used when the launcher dials the
	// publisher. Empty falls back to "postgres" so the loopback /
	// trust auth path matches existing replication setups.
	User string
}

// LaunchApplyWorkerFunc is the per-subscription dial+run hook. The
// returned error is logged; the launcher itself does not restart the
// worker — M0103-0003's reconnect loop owns retry policy.
type LaunchApplyWorkerFunc func(ctx context.Context, cfg ApplyLauncherConfig, sub catalog.Subscription) error

const defaultLauncherPoll = 10 * time.Second

// ApplyLauncher is one process-global background worker that tracks
// enabled subscriptions and spawns apply workers on demand. Construct
// with NewApplyLauncher and drive with Run. Close cancels every
// in-flight worker and waits for them to exit.
type ApplyLauncher struct {
	cfg ApplyLauncherConfig

	mu      sync.Mutex
	workers map[string]*launchedWorker // keyed by Subscription.Name

	wake chan struct{}
}

type launchedWorker struct {
	ctx    context.Context
	cancel context.CancelFunc
	done   chan struct{}
}

// NewApplyLauncher wires the launcher to the runtime's catalog +
// storage handles. PubSub must be non-nil — without it the launcher
// has no subscriptions to scan.
func NewApplyLauncher(cfg ApplyLauncherConfig) *ApplyLauncher {
	if cfg.PollInterval <= 0 {
		cfg.PollInterval = defaultLauncherPoll
	}
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}
	if cfg.LaunchFn == nil {
		cfg.LaunchFn = DefaultLaunchApplyWorker
	}
	if cfg.User == "" {
		cfg.User = "postgres"
	}
	return &ApplyLauncher{
		cfg:     cfg,
		workers: map[string]*launchedWorker{},
		wake:    make(chan struct{}, 1),
	}
}

// Wake nudges the launcher to rescan immediately. Safe to call from
// any goroutine; coalesces multiple wakeups into a single rescan.
// Called from execCreateSubscription / execDropSubscription so DDL
// becomes observable in pg_stat_subscription within ≤PollInterval ms
// instead of waiting for the periodic tick.
func (l *ApplyLauncher) Wake() {
	if l == nil {
		return
	}
	select {
	case l.wake <- struct{}{}:
	default:
	}
}

// Run drives the reconcile loop until ctx is cancelled. On exit it
// cancels every per-worker context and waits for each worker
// goroutine to return.
func (l *ApplyLauncher) Run(ctx context.Context) {
	if l == nil || l.cfg.PubSub == nil {
		return
	}
	defer l.stopAll()

	t := time.NewTimer(l.cfg.PollInterval)
	defer t.Stop()

	l.reconcile(ctx)

	for {
		select {
		case <-ctx.Done():
			return
		case <-l.wake:
			if !t.Stop() {
				select {
				case <-t.C:
				default:
				}
			}
			l.reconcile(ctx)
			t.Reset(l.cfg.PollInterval)
		case <-t.C:
			l.reconcile(ctx)
			t.Reset(l.cfg.PollInterval)
		}
	}
}

// reconcile compares the catalog's enabled-subscription set against
// the running-worker set and launches / stops workers to match.
// Holds the launcher mutex for the duration of the scan; the actual
// per-worker goroutine spawn is deferred until after the lock is
// released so a slow dial cannot stall a CREATE/DROP DDL caller.
func (l *ApplyLauncher) reconcile(ctx context.Context) {
	subs := l.cfg.PubSub.Subscriptions()

	enabled := make(map[string]catalog.Subscription, len(subs))
	for _, s := range subs {
		if s == nil || !s.Enabled {
			continue
		}
		enabled[s.Name] = *s
	}

	var toStop []*launchedWorker
	var toStart []catalog.Subscription

	l.mu.Lock()
	for name, w := range l.workers {
		if _, ok := enabled[name]; !ok {
			toStop = append(toStop, w)
			delete(l.workers, name)
		}
	}
	for name, sub := range enabled {
		if _, ok := l.workers[name]; ok {
			continue
		}
		workerCtx, cancel := context.WithCancel(ctx)
		lw := &launchedWorker{ctx: workerCtx, cancel: cancel, done: make(chan struct{})}
		l.workers[name] = lw
		toStart = append(toStart, sub)
	}
	l.mu.Unlock()

	for _, w := range toStop {
		w.cancel()
		<-w.done
	}

	for _, sub := range toStart {
		l.mu.Lock()
		lw := l.workers[sub.Name]
		l.mu.Unlock()
		if lw == nil {
			// Removed by a concurrent reconcile cycle; nothing
			// to do.
			continue
		}
		go l.runWorker(lw, sub)
	}
}

func (l *ApplyLauncher) runWorker(lw *launchedWorker, sub catalog.Subscription) {
	defer close(lw.done)
	err := l.cfg.LaunchFn(lw.ctx, l.cfg, sub)
	if err != nil && !errors.Is(err, context.Canceled) {
		l.cfg.Logger.Warn("apply worker exited", "subscription", sub.Name, "err", err)
	}
	// Clear self from the workers map so the next reconcile cycle
	// can re-launch us if the subscription is still enabled. Guard
	// the deletion behind an identity check because a concurrent
	// stop path may have already replaced our entry or unlinked it.
	l.mu.Lock()
	if cur, ok := l.workers[sub.Name]; ok && cur == lw {
		delete(l.workers, sub.Name)
	}
	l.mu.Unlock()
}

// stopAll cancels every running worker and waits for them to exit.
// Called from Run's defer on ctx cancellation.
func (l *ApplyLauncher) stopAll() {
	l.mu.Lock()
	workers := l.workers
	l.workers = map[string]*launchedWorker{}
	l.mu.Unlock()
	for _, w := range workers {
		w.cancel()
	}
	for _, w := range workers {
		<-w.done
	}
}

// ActiveSubscriptions returns the names of subscriptions with a live
// worker. Test-only observability hook; the public pg_stat_subscription
// view reads the same set through PubSub.
func (l *ApplyLauncher) ActiveSubscriptions() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	out := make([]string, 0, len(l.workers))
	for name := range l.workers {
		out = append(out, name)
	}
	return out
}

// DefaultLaunchApplyWorker is the production hook: it parses the
// subscription's conninfo, opens a logical-replication receiver
// against the publisher, and runs the apply loop until ctx is
// cancelled or the link breaks. Per-error retry is M0103-0003's
// concern; this loop's caller (the launcher) will respawn us on the
// next reconcile cycle if the worker exits while the subscription is
// still enabled.
func DefaultLaunchApplyWorker(ctx context.Context, cfg ApplyLauncherConfig, sub catalog.Subscription) error {
	addr, appName := parseSubscriptionConninfo(sub.Conninfo)
	if addr == "" {
		return fmt.Errorf("apply worker %q: conninfo has no host:port", sub.Name)
	}
	appName = resolveApplyWorkerApplicationName(appName, sub.Name)

	apply := executor.NewApplyWorker(cfg.Catalog, cfg.Pool, cfg.TxnMgr)
	apply.SetSubscriptionContext(cfg.PubSub, sub.Name)
	apply.SetLogger(cfg.Logger)

	slotName := sub.SlotName
	if slotName == "" {
		slotName = sub.Name
	}
	var startLSN uint64
	if cfg.Slots != nil {
		if slot, err := cfg.Slots.Get(slotName); err == nil {
			startLSN = slot.ConfirmedFlushLSN
		}
	}

	// Use NewLogicalReceiver (not DialLogicalReceiver): the
	// receiver's Run loop owns the entire connect / reconnect
	// lifecycle so a publisher restart or transient network blip
	// no longer terminates the apply worker (M0103-0003).
	rec := NewLogicalReceiver(LogicalReceiverConfig{
		PrimaryAddr:     addr,
		User:            cfg.User,
		ApplicationName: appName,
		SlotName:        slotName,
		Publications:    sub.Publications,
		StartLSN:        startLSN,
		Apply:           apply,
	})
	defer apply.SafeRollback()
	return rec.Run(ctx)
}

// resolveApplyWorkerApplicationName picks the application_name the
// apply worker should advertise in its libpq startup packet. The
// conninfo's explicit `application_name=...` wins; otherwise we fall
// back to the subscription name itself. Empty subscription names yield
// an empty string — callers may treat that as "no application_name
// parameter sent". Mirrors upstream libpqrcv's
// `walrcv_application_name` semantics so PG's `pg_stat_replication`
// keys on a stable, predictable identifier and any
// `synchronous_standby_names = '<sub>'` rule matches by default.
func resolveApplyWorkerApplicationName(parsedAppName, subName string) string {
	if parsedAppName != "" {
		return parsedAppName
	}
	return subName
}

// parseSubscriptionConninfo extracts host:port and the application_name
// from a libpq-style key=value bag. Mirrors parsePrimaryConninfoFull
// in cmd/goopg but is duplicated here so internal/server doesn't need
// a cmd/-package import.
func parseSubscriptionConninfo(conninfo string) (addr, appName string) {
	conninfo = strings.TrimSpace(conninfo)
	if conninfo == "" {
		return "", ""
	}
	host := ""
	port := "5432"
	for _, tok := range strings.Fields(conninfo) {
		eq := strings.IndexByte(tok, '=')
		if eq < 0 {
			continue
		}
		k := strings.ToLower(tok[:eq])
		v := tok[eq+1:]
		switch k {
		case "host":
			host = v
		case "port":
			port = v
		case "application_name":
			appName = v
		}
	}
	if host == "" {
		return "", appName
	}
	return host + ":" + port, appName
}
