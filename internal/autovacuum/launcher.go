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

	// NapInterval is the time between launcher wakeups.
	NapInterval time.Duration

	// WorkerLimit caps the number of concurrent vacuum/analyze workers.
	WorkerLimit int

	logger *slog.Logger
}

// NewLauncher creates a Launcher with the given dependencies.
func NewLauncher(pool *storage.Pool, txnMgr *mvcc.Manager, cat catalog.Catalog) *Launcher {
	return &Launcher{
		Pool:        pool,
		TxnMgr:      txnMgr,
		Cat:         cat,
		NapInterval: 60 * time.Second,
		WorkerLimit: 1,
	}
}

// SetLogger attaches a structured logger.
func (l *Launcher) SetLogger(logger *slog.Logger) {
	l.logger = logger
}

// Run starts the launcher loop. It blocks until ctx is cancelled.
func (l *Launcher) Run(ctx context.Context) error {
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

	// Get all user tables.
	tables := l.loadTables()
	if len(tables) == 0 {
		return
	}

	// For each table, check if it needs vacuum or analyze.
	for _, tbl := range tables {
		select {
		case <-ctx.Done():
			return
		default:
		}

		rel := l.Cat.RelFileNode(tbl)
		if l.needsVacuum(tbl) {
			log.Info("autovacuum: running vacuum", "table", tbl.Name)
			stats, err := vacuum.Vacuum(l.Pool, l.TxnMgr, rel)
			if err != nil {
				log.Error("autovacuum: vacuum failed", "table", tbl.Name, "error", err)
			} else {
				log.Info("autovacuum: vacuum done", "table", tbl.Name,
					"pages", stats.Pages, "dead", stats.Dead, "live", stats.Live)
			}
		}
		if l.needsAnalyze(tbl) {
			log.Info("autovacuum: running analyze", "table", tbl.Name)
			stats, err := vacuum.Analyze(l.Pool, l.TxnMgr, rel)
			if err != nil {
				log.Error("autovacuum: analyze failed", "table", tbl.Name, "error", err)
			} else {
				log.Info("autovacuum: analyze done", "table", tbl.Name,
					"rows", stats.Rows)
			}
		}
	}
}

// loadTables returns all non-virtual user tables from the catalog.
func (l *Launcher) loadTables() []*catalog.Table {
	if c, ok := l.Cat.(*catalog.InMemory); ok {
		return c.AllTables()
	}
	return nil
}

// needsVacuum returns true if the table's dead-tuple estimate exceeds
// the v0 threshold. For now, always vacuums every nap interval.
func (l *Launcher) needsVacuum(tbl *catalog.Table) bool {
	return true
}

// needsAnalyze returns true if the table's stats are stale. For now,
// always analyzes every nap interval.
func (l *Launcher) needsAnalyze(tbl *catalog.Table) bool {
	return true
}
