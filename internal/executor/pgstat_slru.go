package executor

// pgstat_slru.go — cumulative SLRU statistics (pg_stat_slru)
// (M0118-0009 `stats` spec, final rung; design 0118-0132).
//
// PostgreSQL exposes per-SLRU cache statistics through the pg_stat_slru view
// (backed by pg_stat_get_slru()). Each SLRU (commit_timestamp, notify,
// subtransaction, …) reports blks_zeroed / blks_hit / blks_read / blks_written /
// blks_exists / flushes / truncates — "fixed-amount" statistics (one fixed set
// of rows, not keyed by object OID). The `stats` isolation spec exercises only
// the notify SLRU's blks_zeroed counter: it LISTENs, fires a large pg_notify(),
// forces a stats flush, and asserts blks_zeroed strictly increased.
//
// goopg has no SLRU-backed async-notification queue (notify.go multiplexes every
// connection in one OS process and keeps an in-memory per-session inbox), so
// there are no real page-zeroing events to count. This file MODELS the notify
// SLRU's blks_zeroed: when a transaction that buffered notifications commits with
// at least one listener present, the server reports the byte length of the
// written queue entries here (RecordNotifyQueueWrite), and we advance a modelled
// queue head, counting the SLRU pages newly zeroed as the head crosses page
// boundaries — exactly the events SimpleLruZeroPage() would record in upstream
// asyncQueueAddEntries(). All other SLRUs and counters stay zero (goopg does not
// model their page traffic), which is sufficient: the spec only reads notify
// blks_zeroed, and the view's other rows/columns are unused by it.
//
// Reads honour stats_fetch_consistency exactly like the function/relation stats
// (rung 4): inside an explicit transaction the value is frozen per-transaction
// under 'cache'/'snapshot' so a concurrent backend's later notify is invisible
// until commit or pg_stat_clear_snapshot(). See fetchSLRURows / funcStatSnapshot.

import (
	"strconv"
	"sync"
)

// slruPageSize is the modelled SLRU page size (BLCKSZ). A new page is "zeroed"
// each time the modelled notify-queue head advances onto a fresh page, mirroring
// QUEUE_PAGESIZE in async.c.
const slruPageSize = 8192

// slruResetTimestamp is the fixed stats_reset value reported for every SLRU row
// (goopg never resets SLRU stats — pg_stat_reset_shared('slru') is unimplemented
// and unused by the spec). Matches the static catalog VirtualRows fallback.
const slruResetTimestamp = "2026-01-01 00:00:00+00"

// slruNames lists the SLRU names reported by pg_stat_slru, in upstream
// slru_names[] order ("other" is always last). Mirrors PostgreSQL 18.3.
var slruNames = []string{
	"commit_timestamp",
	"multixact_member",
	"multixact_offset",
	"notify",
	"serializable",
	"subtransaction",
	"transaction",
	"other",
}

// slruCounters holds the seven cumulative counters of one SLRU row.
type slruCounters struct {
	blksZeroed  int64
	blksHit     int64
	blksRead    int64
	blksWritten int64
	blksExists  int64
	flushes     int64
	truncates   int64
}

// slruStatsManager is the process-global SLRU-stats store. goopg models only the
// notify SLRU's blks_zeroed (the sole counter the spec exercises); the modelled
// queue head tracks how far the notify queue has been written so page crossings
// can be counted faithfully.
type slruStatsManager struct {
	mu               sync.Mutex
	notifyBlksZeroed int64
	notifyQueueHead  int64 // running byte offset into the modelled notify queue
}

var slruStats = &slruStatsManager{}

// RecordNotifyQueueWrite reports that a committing transaction wrote queueBytes
// bytes of notification entries to the async queue (with at least one listener
// present). It advances the modelled queue head and increments the notify SLRU's
// blks_zeroed by the number of pages newly zeroed as the head crosses page
// boundaries — the SimpleLruZeroPage() events upstream asyncQueueAddEntries()
// would record. Called by the server at COMMIT (publishPendingNotify); no-op for
// queueBytes <= 0. Exported because notification publication is server-side.
// M0118-0009 (`stats`).
func RecordNotifyQueueWrite(queueBytes int64) {
	if queueBytes <= 0 {
		return
	}
	slruStats.mu.Lock()
	defer slruStats.mu.Unlock()
	before := slruStats.notifyQueueHead
	after := before + queueBytes
	slruStats.notifyQueueHead = after
	// Pages whose index changes as the head moves from `before` to `after` are
	// freshly zeroed. The page containing `before` was already zeroed by an
	// earlier write, unless the queue was previously empty (before == 0), in
	// which case the first page is zeroed now too.
	zeroed := (after-1)/slruPageSize - before/slruPageSize
	if before == 0 {
		zeroed++
	}
	if zeroed > 0 {
		slruStats.notifyBlksZeroed += zeroed
	}
}

// snapshotAll returns a by-name copy of every SLRU row's counters, used to build
// a per-transaction stats snapshot ('cache'/'snapshot' consistency). Mirrors
// pgstat_build_snapshot_fixed copying the whole fixed-amount SLRU array.
func (m *slruStatsManager) snapshotAll() map[string]slruCounters {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make(map[string]slruCounters, len(slruNames))
	for _, name := range slruNames {
		var c slruCounters
		if name == "notify" {
			c.blksZeroed = m.notifyBlksZeroed
		}
		out[name] = c
	}
	return out
}

// slruRowStrings renders one SLRU row's counters as the nine string cells the
// pg_stat_slru virtual view expects (name + 7 counters + stats_reset).
func slruRowStrings(name string, c slruCounters) []string {
	return []string{
		name,
		strconv.FormatInt(c.blksZeroed, 10),
		strconv.FormatInt(c.blksHit, 10),
		strconv.FormatInt(c.blksRead, 10),
		strconv.FormatInt(c.blksWritten, 10),
		strconv.FormatInt(c.blksExists, 10),
		strconv.FormatInt(c.flushes, 10),
		strconv.FormatInt(c.truncates, 10),
		slruResetTimestamp,
	}
}

// fetchSLRURows returns the pg_stat_slru rows honouring the session's
// stats_fetch_consistency. Outside an explicit transaction (or with 'none') it
// reads the live shared counters. Inside an explicit transaction it consults the
// per-transaction snapshot so the rows read stably until commit / clear:
//   - 'cache'    — the whole SLRU set is cached on the first SLRU access.
//   - 'snapshot' — the first access to ANY cumulative stat (function or SLRU)
//     freezes the entire store, so a variable-amount access freezes SLRU too.
//
// The distinction only matters across statements of one transaction, which is
// why a snapshot is built only inside an explicit transaction (mirrors
// fetchFuncStat). M0118-0009 (`stats`).
func fetchSLRURows(ctx *Context) [][]string {
	live := func() [][]string {
		all := slruStats.snapshotAll()
		rows := make([][]string, 0, len(slruNames))
		for _, name := range slruNames {
			rows = append(rows, slruRowStrings(name, all[name]))
		}
		return rows
	}
	sess, _ := ctx.Session.(*BasicSession)
	if sess == nil || !sess.InExplicitTransaction() {
		return live()
	}
	mode := "none"
	if ctx.GetSetting != nil {
		if v, ok := ctx.GetSetting("stats_fetch_consistency"); ok {
			mode = v
		}
	}
	var frozen map[string]slruCounters
	switch mode {
	case "cache":
		snap := sess.ensureStatsSnapshot()
		if snap.slruCache == nil {
			snap.slruCache = slruStats.snapshotAll()
		}
		frozen = snap.slruCache
	case "snapshot":
		snap := sess.ensureStatsSnapshot()
		ensureFullSnapshot(snap)
		frozen = snap.slruFrozen
	default: // "none"
		return live()
	}
	rows := make([][]string, 0, len(slruNames))
	for _, name := range slruNames {
		rows = append(rows, slruRowStrings(name, frozen[name]))
	}
	return rows
}
