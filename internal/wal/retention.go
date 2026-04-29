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
// goopg's v0 retention follows the same shape, minus min_wal_size
// preallocation (we delete instead of recycle-by-rename) and minus
// archive-mode integration (no pg_wal/archive_status yet).
//
// See docs/design/0005-0004-slot-aware-wal-retention.md.

package wal

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
		var err error
		invalidated, err = r.Slots.InvalidateLagging(currentLSN, r.MaxSlotKeepBytes)
		if err != nil {
			r.logger().Warn("wal retention: slot invalidation failed", "err", err)
			// Fall through — we still want to unlink whatever
			// the unchanged slot horizon allows.
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

	// Step 4: unlink obsolete segments. The writer guarantees the
	// segment containing keepLSN is preserved.
	removed, err := r.Writer.RemoveOldSegments(keepLSN)
	if err != nil {
		return fmt.Errorf("wal retention: remove old segments: %w", err)
	}

	if removed > 0 || len(invalidated) > 0 {
		r.logger().Info("wal retention: pruned",
			"checkpoint_lsn", checkpointLSN,
			"keep_lsn", keepLSN,
			"segments_removed", removed,
			"slots_invalidated", invalidated,
		)
	}
	return nil
}

func (r *SlotAwareRetainer) logger() *slog.Logger {
	if r.Logger != nil {
		return r.Logger
	}
	return slog.Default()
}
