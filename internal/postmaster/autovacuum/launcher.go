// Package autovacuum implements goopg's background autovacuum
// launcher and worker infrastructure (M0019).
//
// v0 provides a ticker-driven launcher that periodically scans the
// catalog for user tables and dispatches vacuum/analyze workers.
package autovacuum

import (
	"context"
	"log/slog"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/goopg/goopg/internal/access/transam"
	"github.com/goopg/goopg/internal/access/transam/multixact"
	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/commands/vacuum"
	"github.com/goopg/goopg/internal/executor"
	"github.com/goopg/goopg/internal/storage"
	"github.com/goopg/goopg/internal/utils/misc"
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

	// GUCs, when non-nil, supplies autovacuum_* / vacuum_freeze_* values;
	// nil falls back to upstream boot defaults.
	GUCs *misc.Registry

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
		MinVacuumAge:  60 * time.Second,
		MinAnalyzeAge: 60 * time.Second,
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

	p := l.params()
	now := time.Now()

	// Collect candidates with decisions, then process anti-wraparound
	// relations first (oldest relfrozenxid), matching upstream's
	// wraparound-at-risk priority (autovacuum.c:1145–1195).
	type cand struct {
		tbl   *catalog.Table
		key   string
		rel   storage.RelFileNode
		wrap  bool
		doVac bool
		doAnl bool
	}
	var cands []cand
	for _, tbl := range tables {
		select {
		case <-ctx.Done():
			return
		default:
		}
		key := tbl.Schema + "." + tbl.Name
		rel := l.Cat.RelFileNode(tbl)
		wrap := l.wraparound(tbl, p)
		enabled := !(tbl.AutovacuumEnabledSet && !tbl.AutovacuumEnabled)
		cands = append(cands, cand{tbl: tbl, key: key, rel: rel, wrap: wrap,
			doVac: wrap || (enabled && l.needsVacuum(tbl)),
			doAnl: enabled && l.needsAnalyze(tbl)})
	}
	sort.SliceStable(cands, func(i, j int) bool {
		if cands[i].wrap != cands[j].wrap {
			return cands[i].wrap
		}
		return cands[i].tbl.RelFrozenXID < cands[j].tbl.RelFrozenXID
	})

	for _, c := range cands {
		select {
		case <-ctx.Done():
			return
		default:
		}

		if c.doVac {
			vacWallOK := true
			if !c.wrap {
				if last, ok := l.lastVacuum[c.key]; ok && now.Sub(last) < l.MinVacuumAge {
					vacWallOK = false
				}
			}
			if vacWallOK {
				l.runVacuum(log, c.tbl, c.key, c.rel, c.wrap, p, now)
			}
		}

		if c.doAnl {
			anlWallOK := true
			if last, ok := l.lastAnalyze[c.key]; ok && now.Sub(last) < l.MinAnalyzeAge {
				anlWallOK = false
			}
			if anlWallOK {
				l.runAnalyze(log, c.tbl, c.key, now)
			}
		}
	}
}

// runVacuum executes one autovacuum pass for a table. Anti-wraparound passes
// run aggressively and ignore VM skips; regular passes skip all-visible pages
// and honor the relfrozenxid skip-guard.
func (l *Launcher) runVacuum(log *slog.Logger, tbl *catalog.Table,
	key string, rel storage.RelFileNode, wrap bool, p avParams, now time.Time) {

	log.Info("autovacuum: running vacuum", "table", key, "wraparound", wrap)
	nextXID := storage.TransactionID(0)
	if l.TxnMgr != nil {
		nextXID = l.TxnMgr.NextXID()
	}

	freezeBelow := freezeCutoff(nextXID, l.OldestXmin(), p, reloptionFreezeMinAge(tbl))
	aggressive := wrap || aggressiveByAge(nextXID, tbl.RelFrozenXID, p)

	vo := vacuum.VacuumOptions{
		FSM: l.FSM, VM: l.VM, FreezeBelow: freezeBelow, Aggressive: aggressive,
		Truncate: true, FailsafeAge: p.failsafeAge,
	}
	// Cost pacing: autovacuum_vacuum_cost_delay defaults to 2 ms upstream;
	// limit -1 defers to vacuum_cost_limit (200).
	dl := int64(2)
	if v, ok := l.gucInt("autovacuum_vacuum_cost_delay"); ok {
		if v < 0 {
			dl = 0
		} else {
			dl = v
		}
	}
	vo.CostDelayMS = dl
	limit := int64(200)
	if v, ok := l.gucInt("autovacuum_vacuum_cost_limit"); ok && v > 0 {
		limit = v
	} else if v, ok := l.gucInt("vacuum_cost_limit"); ok && v > 0 {
		limit = v
	}
	vo.CostLimit = limit
	vo.CostPageHit, vo.CostPageMiss, vo.CostPageDirty = 1, 2, 20
	stats, err := vacuum.VacuumWithOptions(l.Pool, l.TxnMgr, rel, vo)
	if err != nil {
		log.Error("autovacuum: vacuum failed", "table", key, "error", err)
		return
	}
	l.lastVacuum[key] = now
	executor.ResetVacuumTriggers(tbl.OID)

	// relfrozenxid skip-guard: non-aggressive passes that skipped
	// visible-not-frozen pages cannot advance (vacuumlazy.c:884–892).
	guardedSkip := !aggressive && stats.SkippedAllVisible > 0
	if freezeBelow > 0 && !guardedSkip {
		switch {
		case stats.NewFrozenXID != 0:
			tbl.RelFrozenXID = stats.NewFrozenXID
		case stats.Frozen > 0:
			tbl.RelFrozenXID = freezeBelow
		}
	}
	log.Info("autovacuum: vacuum done", "table", key,
		"pages", stats.Pages, "dead", stats.Dead, "live", stats.Live,
		"skipped_visible", stats.SkippedAllVisible,
		"skipped_frozen", stats.SkippedAllFrozen, "frozen", stats.Frozen)
}

// runAnalyze executes one autoanalyze pass using the executor-grade sampled
// analyzer so planner-grade column statistics are produced (parity bundle F5).
// The catalog sidecar TableStats is what the planner reads; pg_statistic heap
// persistence requires a session Context and waits for a manual ANALYZE
// (documented deviation).
func (l *Launcher) runAnalyze(log *slog.Logger, tbl *catalog.Table,
	key string, now time.Time) {

	log.Info("autovacuum: running analyze", "table", key)
	ts, err := executor.AnalyzeRelationSampled(l.Pool, l.TxnMgr, l.Cat, tbl)
	if err != nil {
		log.Error("autovacuum: analyze failed", "table", key, "error", err)
		return
	}
	tbl.Stats = ts
	l.lastAnalyze[key] = now
	executor.ResetAnalyzeTriggers(tbl.OID)
	log.Info("autovacuum: analyze done", "table", key,
		"rows", ts.RowCount, "pages", ts.Pages, "columns", len(ts.Columns))
}

// avParams is one tick's snapshot of the trigger/freeze parameters, from GUCs
// when l.GUCs is set, else upstream boot defaults.
type avParams struct {
	vacThresh      int64
	vacScale       float64
	anlThresh      int64
	anlScale       float64
	insThresh      int64
	insScale       float64
	maxThreshold   int64
	freezeMaxAge   int64
	freezeMinAge   int64
	freezeTableAge int64
	failsafeAge    int64
}

func (l *Launcher) params() avParams {
	p := avParams{
		vacThresh: 50, vacScale: 0.2,
		anlThresh: 50, anlScale: 0.1,
		insThresh: 1000, insScale: 0.2,
		maxThreshold: 200_000_000,
		freezeMaxAge: 200_000_000, freezeMinAge: 50_000_000, freezeTableAge: 150_000_000,
		failsafeAge: 1_600_000_000,
	}
	g := func(name string) (int64, bool) {
		if l.GUCs == nil {
			return 0, false
		}
		v, ok := l.GUCs.Get(name)
		if !ok {
			return 0, false
		}
		n, err := strconv.ParseInt(strings.TrimSpace(v.Value), 10, 64)
		if err != nil {
			return 0, false
		}
		return n, true
	}
	gf := func(name string) (float64, bool) {
		if l.GUCs == nil {
			return 0, false
		}
		v, ok := l.GUCs.Get(name)
		if !ok {
			return 0, false
		}
		fv, err := strconv.ParseFloat(strings.TrimSpace(v.Value), 64)
		if err != nil {
			return 0, false
		}
		return fv, true
	}
	if v, ok := g("autovacuum_vacuum_threshold"); ok {
		p.vacThresh = v
	}
	if v, ok := gf("autovacuum_vacuum_scale_factor"); ok {
		p.vacScale = v
	}
	if v, ok := g("autovacuum_analyze_threshold"); ok {
		p.anlThresh = v
	}
	if v, ok := gf("autovacuum_analyze_scale_factor"); ok {
		p.anlScale = v
	}
	if v, ok := g("autovacuum_vacuum_insert_threshold"); ok {
		p.insThresh = v
	}
	if v, ok := gf("autovacuum_vacuum_insert_scale_factor"); ok {
		p.insScale = v
	}
	if v, ok := g("autovacuum_vacuum_max_threshold"); ok && v > 0 {
		p.maxThreshold = v
	}
	if v, ok := g("autovacuum_freeze_max_age"); ok && v > 0 {
		p.freezeMaxAge = v
	}
	if v, ok := g("vacuum_freeze_min_age"); ok && v >= 0 {
		p.freezeMinAge = v
	}
	if v, ok := g("vacuum_failsafe_age"); ok && v > 0 {
		p.failsafeAge = v
	}
	if v, ok := g("vacuum_freeze_table_age"); ok && v > 0 {
		fta := v
		if cap95 := p.freezeMaxAge * 95 / 100; fta > cap95 { // vacuum.c:1246
			fta = cap95
		}
		p.freezeTableAge = fta
	}
	return p
}

func (l *Launcher) gucInt(name string) (int64, bool) {
	if l.GUCs == nil {
		return 0, false
	}
	v, ok := l.GUCs.Get(name)
	if !ok {
		return 0, false
	}
	n, err := strconv.ParseInt(strings.TrimSpace(v.Value), 10, 64)
	if err != nil {
		return 0, false
	}
	return n, true
}

// OldestXmin returns the current cluster-wide oldest xmin (nil-safe).
func (l *Launcher) OldestXmin() storage.TransactionID {
	if l.TxnMgr == nil {
		return 0
	}
	return l.TxnMgr.OldestXmin()
}

// freezeCutoff mirrors upstream FreezeLimit:
// nextXID − min(freeze_min_age, freeze_max_age/2), clamped ≤ OldestXmin
// ALWAYS — an age-0 FREEZE limit must clamp to OldestXmin, never nextXID
// (vacuum.c:1203–1215). reloptionMinAge overrides the GUC min-age when set.
func freezeCutoff(nextXID, oldestXmin storage.TransactionID, p avParams, reloptionMinAge int) storage.TransactionID {
	eff := p.freezeMinAge
	if reloptionMinAge > 0 {
		eff = int64(reloptionMinAge)
	}
	if capHalf := p.freezeMaxAge / 2; eff > capHalf {
		eff = capHalf
	}
	// Compute in SIGNED space. `nextXID - eff` in uint32 wraps when the
	// cluster is younger than freeze_min_age (the normal case for the first
	// ~50M transactions of any cluster's life), and the `fb > oldestXmin`
	// clamp below then silently rewrote that near-4-billion value to
	// oldestXmin — i.e. goopg froze EVERY dead-to-all tuple on every pass,
	// where PG freezes none. (The `fb < 0` test that was meant to catch this
	// is dead: storage.TransactionID is uint32.)
	//
	// Upstream keeps the wrapped value and compares it with
	// TransactionIdPrecedes (vacuum.c:1204-1209), whose modular ordering puts
	// it freeze_min_age transactions in the PAST, so nothing qualifies. goopg's
	// consumer compares plainly (`xmin < FreezeBelow`, commands/vacuum), so the
	// same outcome is expressed as "no freezing this pass": FreezeBelow == 0
	// disables freezing. (review/260831-2 CP-3)
	signed := int64(nextXID) - eff
	if signed < int64(storage.FrozenTransactionID)+1 {
		return 0
	}
	fb := storage.TransactionID(signed)
	if fb > oldestXmin {
		fb = oldestXmin
	}
	return fb
}

// reloptionFreezeMinAge extracts the table's WITH (autovacuum_freeze_min_age)
// override; 0 when unset (caller falls back to the GUC).
func reloptionFreezeMinAge(tbl *catalog.Table) int {
	if tbl.AutovacuumFreezeMinAgeSet {
		return tbl.AutovacuumFreezeMinAge
	}
	return 0
}

// aggressiveByAge reports whether a table's relfrozenxid age has reached the
// freeze_table_age cutoff. InvalidTransactionID/0 counts as INFINITE age —
// upstream creates heaps with relfrozenxid = InvalidTransactionId (heap.c:325)
// so a never-vacuumed table's first pass is aggressive (vacuum.c:1247–1249).
func aggressiveByAge(nextXID, relFrozenXID storage.TransactionID, p avParams) bool {
	if p.freezeTableAge <= 0 {
		return true
	}
	var age int64
	switch {
	case relFrozenXID == storage.InvalidTransactionID || relFrozenXID == 0:
		age = int64(nextXID)
	default:
		if nextXID > relFrozenXID {
			age = int64(nextXID - relFrozenXID)
		}
	}
	return age >= p.freezeTableAge
}

// wraparound reports the anti-wraparound emergency condition:
// relfrozenxid older than nextXID − autovacuum_freeze_max_age
// (autovacuum.c:3056–3062). Never-set relfrozenxid does NOT trigger here —
// upstream guards with TransactionIdIsNormal (autovacuum.c:3064).
func (l *Launcher) wraparound(tbl *catalog.Table, p avParams) bool {
	if l.TxnMgr == nil || tbl.RelFrozenXID == storage.InvalidTransactionID || tbl.RelFrozenXID == 0 {
		return false
	}
	nextXID := l.TxnMgr.NextXID()
	return nextXID > tbl.RelFrozenXID &&
		int64(nextXID-tbl.RelFrozenXID) > p.freezeMaxAge
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

// needsVacuum reports whether the table crossed its vacuum trigger:
// dead_tuples > threshold + scale_factor·reltuples (capped by
// autovacuum_vacuum_max_threshold) OR insert-only growth past its pair.
// Anti-wraparound forcing lives in wrap(); the autovacuum_enabled=off bypass
// is handled by the caller so this predicate stays purely numeric
// (autovacuum.c:3082–3087 handles the override ordering).
func (l *Launcher) needsVacuum(tbl *catalog.Table) bool {
	p := l.params()
	if l.wraparound(tbl, p) {
		return true
	}
	dead, ins, _ := executor.TriggerSnapshot(tbl.OID)
	reltuples := float64(0)
	if tbl.Stats != nil && tbl.Stats.RowCount > 0 {
		reltuples = float64(tbl.Stats.RowCount)
	}
	vacthresh := float64(p.vacThresh) + p.vacScale*reltuples
	if float64(p.maxThreshold) < vacthresh {
		vacthresh = float64(p.maxThreshold)
	}
	if float64(dead) > vacthresh {
		return true
	}
	insthresh := float64(p.insThresh) + p.insScale*reltuples
	// pcnt_unfrozen factor := 1 (goopg tracks no relallfrozen stat;
	// documented deviation in the parity bundle).
	return float64(ins) > insthresh
}

// needsAnalyze reports whether modifications since the last analyze crossed
// threshold + scale_factor·reltuples.
func (l *Launcher) needsAnalyze(tbl *catalog.Table) bool {
	p := l.params()
	_, _, mod := executor.TriggerSnapshot(tbl.OID)
	reltuples := float64(0)
	if tbl.Stats != nil && tbl.Stats.RowCount > 0 {
		reltuples = float64(tbl.Stats.RowCount)
	}
	return float64(mod) > float64(p.anlThresh)+p.anlScale*reltuples
}
