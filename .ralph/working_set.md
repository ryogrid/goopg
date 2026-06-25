(idle — nothing in flight)

Last landed (loop #74): `stats` rung 5 (M0118-0009, design 0118-0127) —
cross-backend two-phase commit for RC/RR prepared transactions. First divergence
advanced **L2036 → L2180**.
Files: internal/mvcc/manager.go (DetachToDedicatedSlot + ErrUnsupportedDetach +
auto-assign Begin bounded to ConnSlotCount), internal/mvcc/procarray.go
(ReservedPreparedSlots=64 + ConnSlotCount consts), internal/server/twophase.go
(preparedXactStore + RC/RR detach in execPrepareTransaction + registry finalise
in execFinalizePrepared), internal/server/conn_tx.go (DetachPrepared),
internal/server/server.go (preparedXacts field + 2× procNum→ConnSlotCount),
internal/server/{copy.go,dispatch_extended.go} (half-offset→ConnSlotCount),
internal/executor/session.go (RelocateTransaction). Tests:
TestDetachToDedicatedSlot{,RejectsSerializable}.
Gates: mvcc unit+race PASS; executor+server+config units PASS;
TestPort_TwoPhaseCommitSameBackend PASS; TestPort_IsolationPreparedTransactions
+ ...CIC strict PASS; stats probe L2036→L2180; build+vet clean; pgbench smoke via
pre-commit hook.

Key insight: goopg ties tx.Handle to a reusable per-backend proc slot, so a
prepared txn that the originating backend keeps working past WILL get its slot
clobbered. Fix = relocate to a RESERVED high-region slot (PG dummy-PGPROC
analogue) + bound all connection/internal allocators to ConnSlotCount.
SERIALIZABLE 2PC kept on the unchanged same-backend keep-open path (SSI state is
Handle-keyed → can't relocate without re-keying); prepared-transactions.spec
never continues the originating backend so it's unaffected.

NEXT rung for `stats` (each Effort-L; spec stays `defer`):
- **L2180 — relation tuple stats**: `pg_stat_get_numscans(oid)` errors "function
  does not exist"; also `pg_stat_get_tuples_{returned,fetched,inserted,...}`,
  `pg_stat_get_live_tuples`/`_dead_tuples`, `pg_stat_get_xact_*` (per-txn). Needs
  a relation-stats counter store fed by seq/index scans + DML, mirroring
  pgstat_relation. Look at the `s_*_table_stats` steps + the seq_scan/n_tup_ins
  column block around stats.spec L2160+.
- Later: SLRU stats (`pg_stat_slru`).
