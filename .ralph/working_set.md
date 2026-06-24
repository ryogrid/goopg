(idle — nothing in flight)

Loop #24: **M0118-0008 CLOSED** — all 25 DDL/VACUUM/maintenance isolation specs are
strict-promoted (`runIsoSpecStrict`) and pass byte-for-byte vs PG 18.3. This loop was
bookkeeping/reconciliation only (no engine change): the D-002 inventory had drifted —
34 strict-passing isolation specs (M0118-0008 group + earlier M0118-0005/0006/0007
promotions) were still marked `failed` in `postgres-oracle-target-inventory.csv`.
Flipped those rows `failed`→`pass` (rationale = test func name), regenerated
`upstream-isolation-coverage.md` + `postgres-oracle-target-inventory.md`, marked
M0118-0008 `[x]`. Smoke: ReindexConcurrentlyToast/AlterTable4/PlpgsqlToast strict PASS.
Isolation tally: 101 pass / 20 failed.

Known residual drift (NOT touched — out of scope, soft `runIsoSpec` not hard-guaranteed):
`aborted-keyrevoke`, `delete-abort-savept-2` are documented PASS in fix_plan (designs
0118-0014/0015) but still `failed` in the CSV. A future M0118-0009 loop should either
promote them to strict or verify+flip the CSV.

Next milestone candidates (each a distinct unbuilt subsystem): M0118-0009 misc
(async-notify=LISTEN/NOTIFY, prepared-transactions=2PC, stats=pg_stat infra,
temp-schema-cleanup, intra-grant-inplace, horizons), M0118-0005 FK
(fk-deadlock/ri-trigger/fk-partitioned-1/2), M0118-0002 predicate-locks
(GIN/GiST/hash AMs + finer SIREAD granularity), M0118-0007 eval-plan-qual (EPQ-over-join).
