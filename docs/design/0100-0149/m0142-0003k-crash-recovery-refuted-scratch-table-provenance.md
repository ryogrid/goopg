# M0142-0003k — crash-recovery hypothesis A refuted; scratch-table provenance narrowed to manual DDL

## Task

Resume point filed by M0142-0003j: pin the root cause of the shared `:65433`
TPC-H bench cluster's `tpch` database losing its entire TPC-H dataset and all
constraints across an unclean-shutdown crash-recovery restart, fix it if it is
a genuine recovery gap (hypothesis A), then reload the cluster.

## Method and evidence, in order

### (a) Scoped throwaway-cluster repro of an unclean shutdown

All three repros used a private, throwaway data directory (`/tmp/goopg-crash-repro`,
port 5533, `scripts/goopg-test-run.sh`-capped) built from the same HEAD binary
(`tmp/goopg-bench-bin`, commit `3c105b4da`) as the shared cluster runs.

1. **Plain unclean kill.** `CREATE TABLE` + 1000 rows + `CHECKPOINT` + 1000
   more rows (uncheckpointed) + `kill -KILL` on the server PID + restart.
   Startup log showed the expected `"database system was not properly shut
   down; automatic recovery in progress"` replay. Result: all 2000 rows
   present, including all 1000 post-checkpoint (WAL-replayed) rows.
2. **`goopg stop -mode immediate`** (the exact CLI shutdown mode the real
   incident used, "no checkpoint, DB_IN_PRODUCTION") in place of `kill -KILL`,
   with another 1000 rows added first. Same clean recovery, same full-data
   survival (3000/3000 rows, all three batches intact).
3. **Mid-flight uncommitted long DDL** — the scenario closest to the real
   incident, where PID 81 was mid-scan on an `ALTER TABLE … ADD CONSTRAINT`
   FK validation when force-stopped. Built `fk_parent` (200k rows) and
   `fk_child` (3M rows, **no index** on the referencing column, to force a
   multi-second sequential-scan validation), started `ALTER TABLE fk_child
   ADD CONSTRAINT fk_child_parent FOREIGN KEY (ref) REFERENCES
   fk_parent(id)` in the background, waited for the server to visibly peg a
   CPU core (~95%) confirming the scan was mid-flight, then `kill -KILL`ed
   the server process. Restart: clean crash-recovery replay, and afterward
   — `fk_parent` = 200000 rows (exact), `fk_child` = 3000000 rows (exact),
   **no** `fk_child_parent` row in `pg_constraint` (the incomplete DDL was
   correctly not applied), and the earlier `crash_repro_t` table from step 1
   still intact at 3000 rows.

All three repros are unclean-shutdown-plus-crash-recovery cycles on the exact
binary and exact CLI shutdown mode (`-mode immediate`) implicated in the
incident, including the specific "kill mid-uncommitted-DDL" shape that PID 81
represented. **All three preserved data exactly and correctly rolled back
incomplete work.** Hypothesis A (a genuine WAL/checkpoint crash-recovery gap)
is refuted for every scenario tested; no repro attempt reproduced the
incident's symptom (wholesale loss of committed data).

### (b) Testport connection-setup read (hypothesis B)

`internal/testport/mergejoin_all_clauses_test.go` and
`internal/testport/lockrows_sort_ctid_test.go` both call `newCluster(t, name)`
(`internal/testport/tap_port_test.go:190-202`), which calls
`cluster.New(name, cluster.Options{RepoRoot: ..., DataDir:
filepath.Join(t.TempDir(), "data"), ...})` with no `ListenAddr` override.
`cluster.New` (`internal/testutil/cluster/cluster.go:94-158`) falls back to
`freePort()` (`net.Listen("tcp","127.0.0.1:0")`, `cluster.go:621-636`) whenever
`ListenAddr` is empty. **Both test files therefore always run against a
fresh, private, per-test data directory and an ephemeral OS-assigned port —
never a fixed port, and never `:65433`.** Neither file references port 65433
or any fixed listen address anywhere. This closes off the literal reading of
hypothesis B ("the automated Go test connected to the shared cluster") as
stated: it cannot have, structurally.

However, the table-name match is not a coincidence to dismiss: `mergejoin_all_clauses_test.go`
literally contains `CREATE TABLE mjq_a (...)` / `CREATE TABLE mjq_b (...)`
(lines 50-51) and `lockrows_sort_ctid_test.go` literally contains `CREATE
TABLE lrs_acct (...)` / `CREATE TABLE lrs_side (...)` (lines 76-77) — the
exact names found orphaned in the shared cluster's `tpch` database
(`mj_a`, `mj_b`, `mjq_a`, `mjq_b`, `lrs_acct`, `lrs_side`, plus `agg_data`,
`tmp1`, `zz_c`, `zz_p1`, `zz_q1`, `zz_tx` from other, unidentified sessions).
The most consistent explanation: **a past loop manually re-ran (or
interactively adapted) this same repro SQL with `psql` directly against the
shared `:65433` cluster** while debugging the underlying merge-join/lock-row-sort
bugs the test files exist to guard against, rather than exercising it only
through `go test` (which would have stayed on an isolated cluster). That is a
process-discipline gap (see `goopg_shared_bench_cluster_collisions` memory —
same class of problem, DDL instead of process kills), not a code defect. The
exact session/command that dropped the *original* TPC-H tables is still not
recoverable — no query log was retained — so this remains the leading
explanation, not a proven one.

## Finding

- **Hypothesis A: refuted.** Three separate unclean-shutdown-plus-recovery
  repros, including one matching the incident's exact shutdown mode and DDL
  shape, all preserved data correctly. Crash recovery is not implicated.
- **Hypothesis B: narrowed, not proven.** The automated test files cannot
  have touched the shared cluster (isolated cluster/port by construction).
  The most likely mechanism is manual/ad hoc reuse of the tests' own repro
  SQL directly against the shared cluster by a past debugging session,
  based on exact table-name matches to two testport files, but the causal
  chain (who/when/why the *original* TPC-H tables were dropped, versus these
  scratch tables merely coexisting) is not reconstructable after the fact.
- **CLAUDE.md's interim caveat (added 2026-09-16 by M0142-0003j) is updated**:
  "persists across restarts" is re-confirmed for the storage engine itself
  (crash recovery is sound); the residual risk is operational (shared-cluster
  DDL discipline), not a durability defect.

## What this does NOT establish

- The exact command/session that dropped the original 8 TPC-H tables and
  all FK/PK constraints.
- Whether the earlier `-0003g`-loop's `pg_basebackup` clone symptom
  (`lineitem` missing in the clone) shares any mechanism with this incident.
- Whether the 12 scratch tables were all created by the same session, or by
  several unrelated debugging sessions over time.

## Reload step blocked pending user decision

Step (c) of this task's resume point — drop the 12 scratch tables and reload
TPC-H SF=1 via HammerDB, then re-land the 11 FK/PK constraints `-0003f`/`-0003g`
already designed — requires DDL against the shared `:65433` cluster's `tpch`
database. A `DROP TABLE` issued against it this loop was declined by the
session's auto-mode classifier ("Modify Shared Resources"), so it was not
attempted further. This step needs either an explicit user go-ahead in a
future loop/session, or the user running it directly. The 11 FK/PK DDL
statements are recorded verbatim in
`docs/design/0100-0149/m0142-0003f-*.md` and
`docs/design/0100-0149/m0142-0003g-fk-index-accelerated-validation.md`
for whoever performs the reload.

## Cross-references

- `.ralph/fix_plan.md` M0142-0003f/g/i/j/k
- `.ralph/deferral_ledger.md` rows `m0142-0003j`, `m0142-0003k`
- `CLAUDE.md` TPC-H section (caveat narrowed this loop)
- `docs/design/0100-0149/m0142-0003j-bench-cluster-data-loss-on-crash-recovery.md`
- `internal/testport/mergejoin_all_clauses_test.go`,
  `internal/testport/lockrows_sort_ctid_test.go`
- `internal/testutil/cluster/cluster.go` (`New`, `freePort`)
- memory `goopg_shared_bench_cluster_collisions`
