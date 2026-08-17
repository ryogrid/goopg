// Package autovacuum implements goopg's background autovacuum
// launcher and worker infrastructure (M0019).
//
// v0 provides a ticker-driven launcher that periodically scans the
// catalog for user tables and dispatches vacuum/analyze workers.
package autovacuum

import (
	"context"
	"log/slog"
	"time"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/access/transam/multixact"
	"github.com/goopg/goopg/internal/access/transam"
	"github.com/goopg/goopg/internal/storage"
	"github.com/goopg/goopg/internal/commands/vacuum"
)

// Launcher dispatches autovacuum and autoanalyze workers at a
// configurable interval.
type Launcher struct {
	Pool   *storage.Pool
	TxnMgr *transam.Manager
	Cat    catalog.Catalog
	// FSM, when non-nil, is updated by each autovacuum pass so
	// subsequent INSERT operations can reuse freed pages (M0046-0003).
	FSM *storage.FSM
	// VM, when non-nil, gets ALL_VISIBLE bits set per page after
	// autovacuum so index-only scans can skip heap fetches (M0046-0004).
	VM *storage.VisibilityMap

	// MultiXact is the process-shared MultiXact member store, passed to
	// vacuum.Analyze so its live-row count resolves an updater-bearing multi
	// xmax to its updater rather than undercounting an only-row-locked tuple.
	// nil disables the multi path (single-holder behaviour). M0118-0003.
	MultiXact *multixact.Store

	// NapInterval is the time between launcher wakeups.
	NapInterval time.Duration

	// MinVacuumAge is the minimum time between vacuums of the same table.
	MinVacuumAge time.Duration

	// MinAnalyzeAge is the minimum time between analyzes of the same table.
	MinAnalyzeAge time.Duration

	// WorkerLimit caps the number of concurrent vacuum/analyze workers.
	WorkerLimit int

	// OnRunStart / OnRunEnd are optional hooks called at the start
	// and end of Run(), so the caller can register the autovacuum
	// launcher goroutine in the activity registry.
	OnRunStart func()
	OnRunEnd   func()

	lastVacuum  map[string]time.Time // key: qualified table name
	lastAnalyze map[string]time.Time

	logger *slog.Logger
}

// NewLauncher creates a Launcher with the given dependencies.
func NewLauncher(pool *storage.Pool, txnMgr *transam.Manager, cat catalog.Catalog) *Launcher {
	return &Launcher{
		Pool:          pool,
		TxnMgr:        txnMgr,
		Cat:           cat,
		NapInterval:   60 * time.Second,
		MinVacuumAge:  5 * time.Minute,
		MinAnalyzeAge: 5 * time.Minute,
		WorkerLimit:   1,
		lastVacuum:    make(map[string]time.Time),
		lastAnalyze:   make(map[string]time.Time),
	}
}

// SetLogger attaches a structured logger.
func (l *Launcher) SetLogger(logger *slog.Logger) {
	l.logger = logger
}

// Run starts the launcher loop. It blocks until ctx is cancelled.
func (l *Launcher) Run(ctx context.Context) error {
	if l.OnRunStart != nil {
		l.OnRunStart()
	}
	if l.OnRunEnd != nil {
		defer l.OnRunEnd()
	}
	log := l.logger
	if log == nil {
		log = slog.Default()
	}
	log.Info("autovacuum launcher starting",
		"nap", l.NapInterval,
		"workers", l.WorkerLimit)

	ticker := time.NewTicker(l.NapInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			log.Info("autovacuum launcher stopped")
			return ctx.Err()
		case <-ticker.C:
			l.tick(ctx, log)
		}
	}
}

func (l *Launcher) tick(ctx context.Context, log *slog.Logger) {
	log.Debug("autovacuum launcher tick")

	tables := l.loadTables()
	if len(tables) == 0 {
		return
	}

	now := time.Now()
	for _, tbl := range tables {
		select {
		case <-ctx.Done():
			return
		default:
		}

		key := tbl.Schema + "." + tbl.Name
		rel := l.Cat.RelFileNode(tbl)

		if last, ok := l.lastVacuum[key]; !ok || now.Sub(last) >= l.MinVacuumAge {
			if l.needsVacuum(tbl) {
				log.Info("autovacuum: running vacuum", "table", key)
				// Compute freeze horizon (M0046-0005).
				var freezeBelow storage.TransactionID
				if l.TxnMgr != nil {
					currentXID := l.TxnMgr.NextXID()
					const freezeMinAge = storage.TransactionID(50_000_000)
					if currentXID > freezeMinAge {
						freezeBelow = currentXID - freezeMinAge
					}
				}
				stats, err := vacuum.VacuumWithOptions(l.Pool, l.TxnMgr, rel, vacuum.VacuumOptions{
					FSM: l.FSM, VM: l.VM, FreezeBelow: freezeBelow,
				})
				if err == nil && freezeBelow > 0 {
					if stats.NewFrozenXID != 0 {
						tbl.RelFrozenXID = stats.NewFrozenXID
					} else if stats.Frozen > 0 {
						tbl.RelFrozenXID = freezeBelow
					}
				}
				_ = stats // suppress "declared but not used" before log below
				if err != nil {
					log.Error("autovacuum: vacuum failed", "table", key, "error", err)
				} else {
					l.lastVacuum[key] = now
					log.Info("autovacuum: vacuum done", "table", key,
						"pages", stats.Pages, "dead", stats.Dead, "live", stats.Live)
				}
			}
		}

		if last, ok := l.lastAnalyze[key]; !ok || now.Sub(last) >= l.MinAnalyzeAge {
			if l.needsAnalyze(tbl) {
				log.Info("autovacuum: running analyze", "table", key)
				stats, err := vacuum.Analyze(l.Pool, l.TxnMgr, rel, l.MultiXact)
				if err != nil {
					log.Error("autovacuum: analyze failed", "table", key, "error", err)
				} else {
					l.lastAnalyze[key] = now
					log.Info("autovacuum: analyze done", "table", key,
						"rows", stats.Rows)
				}
			}
		}
	}
}

// loadTables returns every user table the launcher should consider for
// autovacuum/analyze. l.Cat may be a wrapper (e.g. *catalog.SearchPathCatalog,
// which scopes lookups to a connection's database) rather than the underlying
// *catalog.InMemory. A bare `l.Cat.(*catalog.InMemory)` assertion silently
// fails on such a wrapper and no-ops autovacuum entirely; instead peel the
// Unwrap() chain until the concrete InMemory catalog is reached — the same
// idiom used in internal/server/dispatch.go and internal/planner/planner.go.
func (l *Launcher) loadTables() []*catalog.Table {
	type unwrapper interface{ Unwrap() catalog.Catalog }
	base := l.Cat
	for {
		if c, ok := base.(*catalog.InMemory); ok {
			return c.AllTables()
		}
		if u, ok := base.(unwrapper); ok {
			base = u.Unwrap()
		} else {
			return nil
		}
	}
}

// autovacuumFreezeMaxAge is the XID age at which autovacuum is forced for
// anti-wraparound protection. Mirrors autovacuum_freeze_max_age default.
const autovacuumFreezeMaxAge = storage.TransactionID(200_000_000)

// needsVacuum returns true if the table should be vacuumed.
// The primary trigger is the regular data-change heuristic; the secondary
// (higher-priority) trigger is XID-age anti-wraparound (M0046-0005).
//
// CLOG truncation (G1) is intentionally NOT driven from this anti-wraparound
// path. Once VACUUM/freeze advances a table's relfrozenxid, the cluster
// datfrozenxid (= min relfrozenxid across user tables, catalog.DatFrozenXID)
// rises, and the single durable-ordered truncation integration point — the
// checkpointer's post-checkpoint TruncateCLOGFn hook (see internal/initdb/
// open.go) — removes the now-dead pg_xact segments AFTER the checkpoint marker
// is on disk. Keeping truncation off the vacuum path avoids removing clog
// status that a not-yet-durable checkpoint might still need on crash recovery.
func (l *Launcher) needsVacuum(tbl *catalog.Table) bool {
	// Anti-wraparound trigger: force vacuum when relfrozenxid is too old.
	// This overrides autovacuum_enabled=off below, matching autovacuum.c's
	// relation_needs_vacanalyze: "But ignore [the user's disable] if at risk".
	if tbl.RelFrozenXID != storage.InvalidTransactionID && l.TxnMgr != nil {
		currentXID := l.TxnMgr.NextXID()
		if currentXID > tbl.RelFrozenXID &&
			currentXID-tbl.RelFrozenXID > autovacuumFreezeMaxAge {
			return true
		}
	}
	if tbl.AutovacuumEnabledSet && !tbl.AutovacuumEnabled {
		return false
	}
	if tbl.Stats == nil {
		return true
	}
	return tbl.Stats.RowCount > 0
}

// needsAnalyze returns true if the table should be analyzed.
func (l *Launcher) needsAnalyze(tbl *catalog.Table) bool {
	if tbl.AutovacuumEnabledSet && !tbl.AutovacuumEnabled {
		return false
	}
	return true
}
