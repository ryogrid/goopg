package mvcc

import "sync/atomic"

// partition_detach_epoch.go — a process-global monotonic counter that orders
// concurrent ALTER TABLE … DETACH PARTITION … CONCURRENTLY events against the
// snapshots that observe them. Design 0118-0058 (M0118-0008).
//
// PostgreSQL implements concurrent detach in two committed phases: phase 1 marks
// the partition pg_inherits.inhdetachpending = true and COMMITS, then waits;
// phase 2 removes the partition. After phase 1 commits, every snapshot taken
// afterwards omits the partition (find_inheritance_children_extended), while a
// snapshot taken before still includes it — the standard MVCC catalog visibility
// rule. goopg keeps a single non-MVCC shared catalog, so it cannot rely on the
// pg_inherits tuple's xmin to order the change. Instead a partition child marked
// detach-pending records the epoch returned by NextPartitionDetachEpoch, every
// snapshot records CurrentPartitionDetachEpoch at acquisition, and the partition
// descriptor omits a child whose stamped epoch is <= the snapshot's epoch (see
// catalog.VisiblePartitionChildren). A READ COMMITTED statement takes a fresh
// snapshot per statement (epoch advances past the detach → partition vanishes);
// a REPEATABLE READ transaction reuses the snapshot captured at BEGIN (epoch
// frozen before the detach → partition stays visible until commit).
var partitionDetachEpoch atomic.Uint64

// NextPartitionDetachEpoch advances and returns the global partition-detach
// epoch. Call once when a partition child transitions to detach-pending so that
// every snapshot acquired afterwards observes the detach. The returned value is
// always >= 1 (the counter starts at 0, which means "no detach observed").
func NextPartitionDetachEpoch() uint64 {
	return partitionDetachEpoch.Add(1)
}

// CurrentPartitionDetachEpoch returns the current global partition-detach epoch
// without advancing it. Captured into every snapshot at acquisition time so the
// partition descriptor can be reconstructed snapshot-consistently.
func CurrentPartitionDetachEpoch() uint64 {
	return partitionDetachEpoch.Load()
}
