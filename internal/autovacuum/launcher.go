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
	"github.com/goopg/goopg/internal/mvcc"
	"github.com/goopg/goopg/internal/storage"
	"github.com/goopg/goopg/internal/vacuum"
)

// Launcher dispatches autovacuum and autoanalyze workers at a
// configurable interval.
type Launcher struct {
	Pool   *storage.Pool
	TxnMgr *mvcc.Manager
	Cat    catalog.Catalog
	// FSM, when non-nil, is updated by each autovacuum pass so
	// subsequent INSERT operations can reuse freed pages (M0046-0003).
	FSM *storage.FSM

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

	lastVacuum  map[string]time.Time   // key: qualified table name
	lastAnalyze map[string]time.Time

	logger *slog.Logger
}

// NewLauncher creates a Launcher with the given dependencies.
func NewLauncher(pool *storage.Pool, txnMgr *mvcc.Manager, cat catalog.Catalog) *Launcher {
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
				stats, err := vacuum.VacuumWithFSM(l.Pool, l.TxnMgr, rel, l.FSM)
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
				stats, err := vacuum.Analyze(l.Pool, l.TxnMgr, rel)
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

func (l *Launcher) loadTables() []*catalog.Table {
	if c, ok := l.Cat.(*catalog.InMemory); ok {
		return c.AllTables()
	}
	return nil
}

// needsVacuum returns true if the table should be vacuumed.
// v0 uses a simple heuristic: vacuum if the table has any
// stats (has been analyzed) and RowCount > 0; otherwise skip
// (no data yet or never analyzed).
func (l *Launcher) needsVacuum(tbl *catalog.Table) bool {
	if tbl.Stats == nil {
		return true
	}
	return tbl.Stats.RowCount > 0
}

// needsAnalyze returns true if the table should be analyzed.
func (l *Launcher) needsAnalyze(tbl *catalog.Table) bool {
	return true
}

