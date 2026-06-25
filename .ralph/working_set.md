(idle — nothing in flight)

Last loop (#59) COMPLETE + committed: M0118-0009 `fk-partitioned-1` ENABLER
(design 0118-0118, NOT a promotion). ATTACH PARTITION now clones a partitioned
parent's FOREIGN KEY onto the attached partition and validates its existing
rows against the referenced table.

Files: internal/executor/operators_fk.go (new `cloneAndValidateAttachPartitionFKs`
+ `fkConstraintName` honours explicit `fk.Name`), operators_ddl.go (call in the
`AlterTableAttachPartition` case, statement-time, before the explicit-txn defer).

Result: all **Class A** `fk-partitioned-1` perms (referenced row deleted
before/during attach → `insert or update on table "pfk1" violates foreign key
constraint "pfk_a_fkey"`, incl. `<waiting ...>`) byte-identical to PG 18.3;
first divergence moved to the first **Class B** perm. Spec stays `defer`.

Class B remaining (ledger 2026-06-25): (1) referenced-side per-partition
enforcement — `delete from ppk1` restricted reporting constraint `pfk_a_fkey_1`
"on table pfk" (PG's cloned referenced-side constraint with `_N` suffix + leaf
name; goopg's `assertNoChildRows` reports declared RefTable + the referencing
table's own name); (2) attach's FOR-KEY-SHARE lock HELD TO COMMIT so a
concurrent `delete from ppk1` `<waiting ...>` behind an uncommitted attach;
(3) secondary `table "pfk1" does not exist` error on Class-B post-attach path.

Remaining M0118 failing isolation specs (all Effort-L unbuilt subsystems):
fk-partitioned-1/2 (Class B above), index-only-bitmapscan (needs real
Bitmap Heap Scan + BitmapOr plan node — goopg has NO bitmap scan; the ONLY
divergence is s1_explain rendering the BitmapOr plan), predicate-gin/gist
(int[]/point types + GIN/GiST AMs), deadlock-parallel (lock-group), stats
(pg_stat_force_next_flush + cumulative stats subsystem), prepared-transactions
{,-cic} (full 2PC SSI).
