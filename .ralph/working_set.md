(idle — nothing in flight)

Last loop (#60) COMPLETE + committed: M0118-0005/0009 `fk-partitioned-1`
referenced-side ENABLER (design 0118-0119, NOT a promotion). A `DELETE FROM ppk1`
(leaf of the *referenced* partitioned `ppk`) is now rejected referenced-side once
`pfk1` is attached as a partition of FK-owner `pfk`:
`update or delete on table "ppk1" violates foreign key constraint "pfk_a_fkey_1"
on table "pfk"`.

Files:
- internal/executor/operators_fk.go — new `enforceFKOnDeletePartitionAncestor`
  (called at end of `enforceFKOnDelete`) + `partitionParentTable` helper. Walks
  the deleted leaf's partition ancestors, fires any FK referencing an ancestor,
  SKIPS per-partition FK clones (`IsPartitionChild`) so it names the ROOT `pfk`,
  appends `<fkname>_<N>` ordinal suffix (`pfk_a_fkey_1`).
- internal/catalog/catalog.go — new `IsPartitionChild` + `PartitionParentOf`
  (map-backed; reliable for ATTACH partitions whose `Table.PartitionParentOID`==0).
- internal/executor/operators_ddl.go — `dropPartitionDescendants` now marks
  cascade-dropped partitions in `cascadeDropped` so teardown
  `DROP TABLE ppk, pfk, pfk1` doesn't raise `table "pfk1" does not exist`.

Result: Class A (0118-0118) + the 4 committed-attach Class B perms byte-identical
to PG 18.3. Probe `defer` 130/133 lines; first divergence = first CONCURRENT
Class B perm (`s1b s2b s2a s1d s2c s1c`). Spec stays `defer`.

NEXT (concurrent Class B, 3 perms — ledger 2026-06-25): `s1d delete from ppk1`
must `<waiting ...>` behind an UNCOMMITTED `s2a attach` and error once it commits.
Needs the attach's referenced-row FOR-KEY-SHARE lock HELD TO COMMIT (today
`cloneAndValidateAttachPartitionFKs`/`scanTableForMatchFKWait` waits on in-flight
deletes but leaves NO lock a later delete blocks on). During the uncommitted
window `IsPartitionChild(pfk1)` is still false (RegisterPartitionChild deferred to
commit) so the referenced-side check fires eagerly + mis-names the clone — both
fixed by adding the lock.

Other remaining M0118 (all Effort-L unbuilt subsystems): fk-partitioned-1 (above)
/ fk-partitioned-2, index-only-bitmapscan (real Bitmap Heap Scan + BitmapOr),
predicate-gin/gist (int[]/point + GIN/GiST AMs), predicate-hash, deadlock-parallel
(lock-group), stats (pg_stat_* cumulative subsystem).
