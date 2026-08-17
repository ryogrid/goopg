// Slot-aware WAL retention: the post-checkpoint hook that decides
// which WAL segments are safe to unlink.
//
// Upstream PostgreSQL drives retention from RemoveOldXlogFiles +
// KeepLogSeg + InvalidateObsoleteReplicationSlots in
// postgres/src/backend/access/transam/xlog.c. The chain is:
//
//   1. Compute the keep-LSN baseline from the just-completed
//      checkpoint (everything strictly before that LSN is durable in
//      data files, so the WAL records are no longer needed for crash
//      recovery).
//   2. For every replication slot, push the keep-LSN backwards to
//      include the slot's RestartLSN so a connected — or recently
//      disconnected — standby can still resume from where it left off.
//   3. Cap that backwards-push by `max_slot_wal_keep_size`: a slot
//      whose lag exceeds the cap is INVALIDATED (loses its hold on
//      WAL) so a single laggy standby can't pin retention forever.
//   4. Unlink every segment whose final byte sits strictly before the
//      segment containing the resulting keep-LSN.
//
// goopg's v0 retention follows the same shape, including min_wal_size/
// max_wal_size/checkpoint-distance-estimate-sized recycle-by-rename
// (Writer.RemoveOldSegmentsWithEstimate, wired via
// CheckPointDistanceEstimateFn below — see
// docs/design/0122-0009-wal-segment-recycling.md), minus archive-mode
// integration (no pg_wal/archive_status yet).
//
// See docs/design/0005-0004-slot-aware-wal-retention.md.

package xlog

import (
	"fmt"
	"log/slog"
)

// Retainer is the seam the Checkpointer calls after a checkpoint
// marker has been appended and flushed. Implementations decide what
// (if anything) to do with the now-superseded WAL.
type Retainer interface {
	// Retain is invoked with the LSN of the just-flushed
	// checkpoint marker (i.e. the upper bound of WAL the
	// checkpointer just made redundant for crash recovery).
	// Implementations must not block the checkpointer for long;
	// the canonical implementation does cheap unlinks only.
	Retain(checkpointLSN uint64) error
}

// SlotAwareRetainer is the production retention policy: honour
// active replication slots, invalidate ones that have lagged past
// max_slot_wal_keep_size, then remove every segment that's no longer
// needed by either crash recovery or any live slot.
type SlotAwareRetainer struct {
	// Writer is the WAL writer whose pg_wal/ directory is being
	// pruned. Required.
	Writer *Writer

	// Slots is the replication-slot registry. May be nil — a nil
	// registry collapses retention to "keep nothing earlier than
	// the checkpoint", which matches the upstream behaviour when
	// no slots exist.
	Slots *Slots

	// MaxSlotKeepBytes mirrors `max_slot_wal_keep_size`. <= 0
	// means unlimited (no slot is ever invalidated for lag).
	// Stored as int64 so the GUC's signed sentinel passes through
	// unchanged.
	MaxSlotKeepBytes int64

	// Logger receives the per-checkpoint retention summary
	// (segments removed, slots invalidated). Defaults to slog
	// default.
	Logger *slog.Logger

	// CheckPointDistanceEstimateFn, when non-nil, returns the current
	// moving-average estimate (bytes) of inter-checkpoint WAL volume —
	// wired to (*Checkpointer).CheckPointDistanceEstimate in production.
	// Combined with CompletionTarget and Writer's Config.MinWALSize/
	// MaxWALSize, it drives RemoveOldSegmentsWithEstimate's
	// XLOGfileslop-style sizing so segment recycling can grow past the
	// MinWALSize floor under sustained write volume (capped at
	// MaxWALSize). nil falls back to RemoveOldSegments' pre-existing
	// MinWALSize-only floor. M0122-0009 follow-up; see
	// docs/design/0122-0009-wal-segment-recycling.md.
	CheckPointDistanceEstimateFn func() float64

	// CompletionTarget mirrors checkpoint_completion_target, one of the
	// XLOGfileslop distance-formula inputs. Only consulted when
	// CheckPointDistanceEstimateFn is non-nil.
	CompletionTarget float64
}

// Retain implements Retainer. Steps mirror the order documented in
// the file header.
func (r *SlotAwareRetainer) Retain(checkpointLSN uint64) error {
	if r == nil || r.Writer == nil || checkpointLSN == 0 {
		return nil
	}

	// Step 1 + 3: invalidate slots whose lag exceeds the cap.
	// Done BEFORE computing the keep horizon so the freshly-
	// invalidated slots stop pinning WAL on this very checkpoint.
	// We use the writer's WrittenLSN (not the checkpoint LSN) as
	// the lag yardstick because lag is measured against the live
	// write head, which is what upstream does — KeepLogSeg uses
	// the current write position, not the checkpoint redo.
	currentLSN := r.Writer.WrittenLSN()
	var invalidated []string
	if r.Slots != nil && r.MaxSlotKeepBytes > 0 {
		// Pre-eviction warning sweep: any slot whose lag has
		// crossed LagWarnFraction of the cap gets an INFO log so
		// the operator can react (raise the cap, fix the standby)
		// before the next checkpoint flips the slot to
		// invalidated. Done before the actual invalidation pass
		// so the warning fires on the same checkpoint where the
		// slot has crossed the threshold but not yet the cap.
		r.warnLaggingSlots(currentLSN)

		var err error
		invalidated, err = r.Slots.InvalidateLagging(currentLSN, r.MaxSlotKeepBytes)
		if err != nil {
			r.logger().Warn("wal retention: slot invalidation failed",
				"event", EventSlotInvalidated, "err", err)
			// Fall through — we still want to unlink whatever
			// the unchanged slot horizon allows.
		}
		// Per-slot WARN for each freshly-invalidated slot so an
		// alert can wake somebody up. The summary log below
		// still names the full list, but a per-slot WARN-level
		// event is what dashboards typically gate alarms on.
		for _, name := range invalidated {
			r.logger().Warn("replication slot invalidated by retention",
				"event", EventSlotInvalidated, "slot", name,
				"max_slot_wal_keep_bytes", r.MaxSlotKeepBytes,
				"current_lsn", currentLSN)
		}
	}

	// Step 2: derive the keep horizon. Start at the checkpoint
	// LSN (everything strictly before is redo-redundant) and pull
	// backwards if any non-invalidated slot needs WAL we'd
	// otherwise drop.
	keepLSN := checkpointLSN
	if r.Slots != nil {
		if minSlot := r.Slots.MinRestartLSN(); minSlot > 0 && minSlot < keepLSN {
			keepLSN = minSlot
		}
	}

	// Step 4: retire obsolete segments (unlink, or recycle-by-rename up to
	// a spares count sized by min_wal_size/max_wal_size and — when a
	// distance estimate is wired — the same XLOGfileslop-style formula
	// upstream uses). The writer guarantees the segment containing
	// keepLSN is preserved.
	var removed, recycled int
	var err error
	if r.CheckPointDistanceEstimateFn != nil {
		removed, recycled, err = r.Writer.RemoveOldSegmentsWithEstimate(keepLSN, r.CheckPointDistanceEstimateFn(), r.CompletionTarget)
	} else {
		removed, recycled, err = r.Writer.RemoveOldSegments(keepLSN)
	}
	if err != nil {
		return fmt.Errorf("wal retention: remove old segments: %w", err)
	}

	if removed > 0 || recycled > 0 || len(invalidated) > 0 {
		r.logger().Info("wal retention: pruned",
			"event", EventWALSegmentsRecycled,
			"checkpoint_lsn", checkpointLSN,
			"keep_lsn", keepLSN,
			"segments_removed", removed,
			"segments_recycled", recycled,
			"slots_invalidated", invalidated,
		)
	}
	return nil
}

// warnLaggingSlots emits an INFO event for any slot whose lag has
// crossed LagWarnFraction of MaxSlotKeepBytes but hasn't yet
// exceeded the cap. Acts as a heads-up so the operator can raise
// the cap or unblock the standby before the slot is invalidated.
// Caller must guarantee r.Slots != nil and r.MaxSlotKeepBytes > 0.
func (r *SlotAwareRetainer) warnLaggingSlots(currentLSN uint64) {
	if r.Slots == nil || r.MaxSlotKeepBytes <= 0 {
		return
	}
	warnThreshold := uint64(float64(r.MaxSlotKeepBytes) * LagWarnFraction)
	if warnThreshold == 0 {
		return
	}
	for _, slot := range r.Slots.List() {
		if slot.Invalidated {
			continue
		}
		if slot.RestartLSN >= currentLSN {
			continue
		}
		lag := currentLSN - slot.RestartLSN
		if lag <= warnThreshold {
			continue
		}
		if lag > uint64(r.MaxSlotKeepBytes) {
			// About to be invalidated below; the WARN log fires
			// from the post-invalidation loop instead so we
			// don't double-log the same slot.
			continue
		}
		r.logger().Info("replication slot lag approaching cap",
			"event", EventSlotLagWarning,
			"slot", slot.Name,
			"lag_bytes", lag,
			"max_slot_wal_keep_bytes", r.MaxSlotKeepBytes,
			"warn_fraction", LagWarnFraction)
	}
}

func (r *SlotAwareRetainer) logger() *slog.Logger {
	if r.Logger != nil {
		return r.Logger
	}
	return slog.Default()
}
