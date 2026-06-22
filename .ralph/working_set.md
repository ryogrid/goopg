Loop #43: M0118-0008 thirteenth promotion (design 0118-0045) — `vacuum-no-cleanup-lock`
PROMOTED to pass (strict). All permutations byte-for-byte vs PG 18.3.

The spec checks `pg_class.relpages|reltuples` track what a non-aggressive VACUUM
observes even while a cursor pins the only heap page. Two gaps fixed:
1. Registered `vacuum_multixact_freeze_min_age` GUC (config/defaults.go +
   postgresql.conf.sample for the TestSampleConfigCoversRegistry parity gate).
2. Virtual pg_class builder (catalog.VirtualRows, the LIVE read path) hard-coded
   relpages/reltuples=0. New `catalog.(*InMemory).UpdateRelStats` (vac_update_relstats
   analog: overwrites Pages/RowCount, MERGES into existing Stats so a prior
   ANALYZE's pg_statistic survives) + interface method; builder reads t.Stats
   (nil ⇒ 0|0 unchanged); vacuumOp.Next publishes after a successful vacuum.

KEY subtlety: reltuples comes from `vacuum.Analyze`'s FRESH-snapshot visible count,
NOT the prune's surviving-line-pointer count (stats.Live). A recently-dead tuple
(deleted+committed but not removable because pinholder holds OldestXmin back)
survives the prune but must be EXCLUDED from reltuples (PG counts HEAPTUPLE_LIVE,
not RECENTLY_DEAD) → 22 vs expected 21 in the pinholder perm. Visible count = 21. ✓

Files: internal/config/defaults.go + postgresql.conf.sample (GUC);
internal/catalog/catalog.go (UpdateRelStats + interface + VirtualRows builder);
internal/executor/operators_vacuum.go (publish via vacuum.Analyze);
internal/catalog/catalog_test.go (TestUpdateRelStatsPreservesColumns);
internal/testport/isolation_port_test.go (TestPort_IsolationVacuumNoCleanupLock);
docs/design/0118-0045-* + README index; fix_plan; D-002 CSV rationale + regen md.

Gates run: TestPort_IsolationVacuumNoCleanupLock strict PASS; sibling
Vacuum{SkipLocked,ConcurrentDrop,Conflict}+FreezeTheDead strict PASS;
TestUpdateRelStatsPreservesColumns; TestSampleConfigCoversRegistry; -race
vacuum/mvcc; executor/catalog/config PASS; go vet clean; ralph-state-guard;
pgbench smoke = pre-commit hook.

Next step (M0118-0008 tail, all real engine work): alter-table-1/2 (parser: FK
`NOT VALID` + `VALIDATE CONSTRAINT`, then ShareRowExclusive/ShareUpdateExclusive
lock semantics — lock infra already exists, ~144 perms); reindex-concurrently-toast
(allow_system_table_mods GUC + pg_toast schema + reltoastrelid + TOAST reindex);
plpgsql-toast (COMMIT inside DO block = PL/pgSQL txn control); partition
ATTACH/DETACH + alter-table-4 need transactional-DDL cross-session visibility.
