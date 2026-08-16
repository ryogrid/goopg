package executor

// memoizeOp — bundle phase S7 (design doc
// docs/design/correlated-subquery-planning/05-memoize-operator.md §5).
//
// A parameterized result cache interposed between the NLI join driver
// and its inner index probe. Per outer row the driver calls
// BindOuter + Rescan; the op evaluates the probe-key expressions
// against the bound outer slot, and either serves a previously stored
// complete result set for that key or forwards the rescan to the child
// index scan, teeing rows into a new entry.
//
// State machine modeled on ExecMemoize
// (postgres/src/backend/executor/nodeMemoize.c:697):
//
//	lookup      first Next after Rescan probes the cache
//	serveCached key found complete → emit stored rows
//	fillCache   key absent → pull child, tee + emit; complete at EOF
//	            (or after the first row in SingleRow mode, :832)
//	passThrough entry abandoned (budget overflow) → stream uncached
//
// Complete entries only: a set is served only if the child ran to
// exhaustion for that key and every row was stored. Eviction is LRU
// via the shared kvcache library; an entry that cannot fit is
// abandoned, never partially served.
import (
	"strings"

	"github.com/goopg/goopg/internal/executor/kvcache"
	"github.com/goopg/goopg/internal/optimizer"
)

// MemoizeStats carries the per-node ANALYZE counters, PG's
// show_memoize_info line shape (Hits/Misses/Evictions/Overflows).
type MemoizeStats struct {
	Hits      int64
	Misses    int64
	Evictions int64
	Overflows int64
}

// memoizeStat returns (lazily allocating) the stats block for m.
func (c *Context) memoizeStat(m *optimizer.Memoize) *MemoizeStats {
	if c == nil {
		return &MemoizeStats{}
	}
	if c.MemoizeStats == nil {
		c.MemoizeStats = make(map[*optimizer.Memoize]*MemoizeStats)
	}
	st, ok := c.MemoizeStats[m]
	if !ok {
		st = &MemoizeStats{}
		c.MemoizeStats[m] = st
	}
	return st
}

const (
	memoizeModeIdle = iota
	memoizeModeServe
	memoizeModeFill
	memoizeModePass
)

// memoEntryRowOverhead is the crude per-row byte-accounting constant
// (slice header + per-datum overhead); string payloads add their
// length. Accounting precision beyond keeping the cache bounded is a
// non-goal.
const memoEntryRowOverhead = 48

type memoizeOp struct {
	plan  *optimizer.Memoize
	child *indexScanOp
	ctx   *Context
	cache *kvcache.Cache
	stats *MemoizeStats

	outerSlot  SlotView
	outerWidth int

	mode      int
	served    []Row
	serveIdx  int
	fillKey   string
	filling   []Row
	fillBytes int64
	emitMS    *MaterializedSlot
}

func newMemoizeOp(p *optimizer.Memoize, child *indexScanOp) *memoizeOp {
	return &memoizeOp{plan: p, child: child}
}

func (o *memoizeOp) Schema() optimizer.Schema { return o.child.Schema() }

func (o *memoizeOp) openPrep(ctx *Context) error {
	o.ctx = ctx
	o.stats = ctx.memoizeStat(o.plan)
	// Byte budget: WorkMem/4 (the subq-cache convention, ch.06 D6.4);
	// WorkMem == 0 → unlimited (kvcache treats limit ≤ 0 as unlimited).
	// Deliberately NOT derived from EstEntries: PG sizes Memoize's byte
	// cap purely from hash_mem too (est_entries only pre-sizes the hash
	// table), and a planner row-size underestimate here would silently
	// turn the cache into an LRU-thrash loop of misses.
	var budget int64
	if ctx.WorkMem > 0 {
		budget = ctx.WorkMem / 4
	}
	o.cache = kvcache.New(budget)
	o.emitMS = SlotFromRow(o.child.Schema(), nil)
	o.mode = memoizeModeIdle
	return o.child.openPrep(ctx)
}

func (o *memoizeOp) BindOuter(slot SlotView, outerWidth int) {
	o.outerSlot = slot
	o.outerWidth = outerWidth
	o.child.BindOuter(slot, outerWidth)
}

// keyExprs returns the live probe-key expressions from the CHILD's
// IndexScan plan node, not the Memoize node's KeyExprs snapshot.
// Pipeline stages after walkRewriteNLI (remap walkers,
// lowerSubPlanParams) may replace exprs through *Expr pointers; the
// child's Key/Keys are what the probe actually binds, so keying the
// cache on them is correct by construction even if the snapshot went
// stale. KeyExprs stays authoritative only for EXPLAIN and the attach
// gate.
func (o *memoizeOp) keyExprs() []optimizer.Expr {
	if len(o.child.plan.Keys) > 0 {
		return o.child.plan.Keys
	}
	if o.child.plan.Key != nil {
		return []optimizer.Expr{o.child.plan.Key}
	}
	return o.plan.KeyExprs
}

// probeKey renders the cache key for the currently bound outer row:
// datumKey of each key expression, NUL-joined. NULL datums render
// distinctly inside datumKey, so a NULL parameter is a valid key (the
// probe legitimately caches its — empty — result).
func (o *memoizeOp) probeKey() (string, error) {
	keys := o.keyExprs()
	parts := make([]string, len(keys))
	for i, ke := range keys {
		v, err := evalExprSlot(ke, o.outerSlot, o.ctx)
		if err != nil {
			return "", err
		}
		parts[i] = datumKey(v)
	}
	return strings.Join(parts, "\x00"), nil
}

func (o *memoizeOp) Rescan(outerSlot SlotView, outerWidth int) error {
	o.outerSlot = outerSlot
	o.outerWidth = outerWidth
	key, err := o.probeKey()
	if err != nil {
		return err
	}
	if v, ok := o.cache.Get(key); ok {
		o.stats.Hits++
		o.served = v.([]Row)
		o.serveIdx = 0
		o.mode = memoizeModeServe
		return nil
	}
	o.stats.Misses++
	o.fillKey = key
	o.filling = o.filling[:0]
	o.fillBytes = 0
	o.mode = memoizeModeFill
	return o.child.Rescan(outerSlot, outerWidth)
}

// storeFilling completes the current entry and publishes it. Eviction
// bookkeeping rides the cache's own counter.
func (o *memoizeOp) storeFilling() {
	rows := make([]Row, len(o.filling))
	copy(rows, o.filling)
	before := o.cache.Evictions()
	o.cache.Put(o.fillKey, rows, o.fillBytes+memoEntryRowOverhead)
	o.stats.Evictions += o.cache.Evictions() - before
	if _, ok := o.cache.Get(o.fillKey); !ok {
		// Oversize entry was refused by the budget — that is the
		// abandon case (served correctly, not cached).
		o.stats.Overflows++
	}
	o.filling = o.filling[:0]
}

func (o *memoizeOp) Next() (TupleSlot, error) {
	switch o.mode {
	case memoizeModeServe:
		if o.serveIdx >= len(o.served) {
			return nil, EOF
		}
		o.emitMS.row = o.served[o.serveIdx]
		o.serveIdx++
		return o.emitMS, nil
	case memoizeModeFill, memoizeModePass:
		slot, err := o.child.Next()
		if err == EOF {
			if o.mode == memoizeModeFill {
				o.storeFilling()
			}
			o.mode = memoizeModeIdle
			return nil, EOF
		}
		if err != nil {
			return nil, err
		}
		if o.mode == memoizeModeFill {
			// Deep-copy and materialize: the child's slot row may be
			// arena-backed and invalidated by its next page
			// (M0073-0004 retention contract — same rule as hash-join
			// build sides and group keys).
			src := slotToRow(slot)
			cp := make(Row, len(src))
			var bytes int64 = memoEntryRowOverhead
			for i, d := range src {
				cp[i] = d.MaterializeArena()
				bytes += 16 + int64(len(datumKey(cp[i])))
			}
			o.filling = append(o.filling, cp)
			o.fillBytes += bytes
			if lim := o.cache.BudgetLimit(); lim > 0 && o.fillBytes > lim {
				// The entry alone exceeds the whole budget: abandon it
				// and stream the rest uncached (PG's overflow
				// behavior — never a partial entry).
				o.filling = o.filling[:0]
				o.fillBytes = 0
				o.stats.Overflows++
				o.mode = memoizeModePass
			} else if o.plan.SingleRow {
				// Unique probe: at most one row per key — complete
				// immediately (nodeMemoize.c:832) so even a partial
				// drain by the parent caches a servable entry.
				o.storeFilling()
				o.mode = memoizeModePass
			}
		}
		return slot, nil
	default:
		return nil, EOF
	}
}

func (o *memoizeOp) Close() error {
	if o.cache != nil {
		o.cache.Clear()
	}
	o.served = nil
	o.filling = nil
	o.mode = memoizeModeIdle
	return o.child.Close()
}
