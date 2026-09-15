Task: M0142-0003k — pin the M0142-0003j bench-cluster data-loss root cause.
Steps (a)/(b)/(d) DONE this loop; (c) reload is BLOCKED pending a human
decision (see below). M0142-0003i stays blocked on (c).

Files: `.ralph/fix_plan.md` (M0142-0003k rewritten with (a)/(b)/(d) findings,
(c) marked blocked). `.ralph/deferral_ledger.md` (new row, task-id
`m0142-0003k`). `docs/design/0100-0149/m0142-0003k-crash-recovery-refuted-scratch-table-provenance.md`
(new). `docs/design/README.md` (indexed). `CLAUDE.md` (TPC-H caveat rewritten:
durability re-confirmed, residual risk is shared-cluster DDL discipline, not
a storage bug). No Go/production code touched — pure recon + one throwaway
test cluster (created and fully torn down) + docs/fix_plan/ledger updates.

Key symbols/paths: `internal/testutil/cluster/cluster.go` (`New`, `freePort`
— proves testport tests always get a private temp-dir + ephemeral port);
`internal/testport/tap_port_test.go:190-202` (`newCluster` helper);
`bench/tpch/build_schema_goopg.sh` (HammerDB reload driver, not yet run);
the shared `:65433` cluster's `tpch` db still has only 12 scratch tables
(`agg_data`, `lrs_acct`, `lrs_side`, `mj_a`, `mj_b`, `mjq_a`, `mjq_b`, `tmp1`,
`zz_c`, `zz_p1`, `zz_q1`, `zz_tx`) and zero TPC-H tables/constraints —
UNCHANGED from last loop, nothing was written to it this loop (the one
DROP TABLE attempt was declined, see below).

Findings this loop: **Hypothesis A (crash-recovery gap) REFUTED.** Ran 3
repros on a throwaway cluster (`:5533`, `/tmp/goopg-crash-repro`, HEAD binary
`tmp/goopg-bench-bin`, since torn down + directory removed): (1) plain
`kill -KILL` after checkpointed+uncheckpointed committed rows — full
survival; (2) `goopg stop -mode immediate` (the incident's exact shutdown
mode) — full survival; (3) `kill -KILL` mid-flight during an uncommitted,
multi-second `ALTER TABLE ADD CONSTRAINT` FK-validation scan on a 3M-row
unindexed child table (closest match to the incident's stuck-PID-81 shape)
— parent/child row counts exact, incomplete constraint correctly absent,
all earlier data intact. Crash recovery is sound in every tested shape;
drop it as a suspect. **Hypothesis B (literal reading) REFUTED, refined
version unproven.** Read `newCluster`/`cluster.New`/`freePort`: the two
suspect testport files (`mergejoin_all_clauses_test.go`,
`lockrows_sort_ctid_test.go`) always get `t.TempDir()` + an OS-ephemeral
port, never `:65433` — cannot have connected to the shared cluster. But both
files literally contain the `CREATE TABLE mjq_a/mjq_b/lrs_acct/lrs_side` SQL
matching the orphaned scratch tables exactly — most-likely explanation is a
past loop manually running that same repro SQL via `psql` directly against
the shared cluster while debugging those bugs (not the automated test), but
this is not provable after the fact (no query log). The actual event that
dropped the original TPC-H tables/constraints remains unidentified.

Next step: **(c) reload is blocked on a human decision, not more
investigation.** A `DROP TABLE agg_data, lrs_acct, ... ;` against the shared
`:65433` cluster's 12 scratch tables was declined this loop by the session's
auto-mode classifier ("Modify Shared Resources") — did not attempt to
bypass it. Whoever picks this up next needs either explicit user
authorization to modify the shared cluster, or to run the reload
themselves: (1) drop the 12 scratch tables, (2) `bench/tpch/build_schema_goopg.sh`
to rebuild TPC-H SF=1 via HammerDB (~12 min), (3) re-land the 11 FK/PK
constraints whose DDL is recorded verbatim in
`docs/design/0100-0149/m0142-0003f-fk-add-blocked-by-unindexed-validation-scan.md`
and `docs/design/0100-0149/m0142-0003g-fk-index-accelerated-validation.md`,
(4) resume M0142-0003i's remaining 5 FKs + Q9 re-measurement. Until that
happens, do NOT run any FK-related DDL against `:65433` (nothing to add it
to) and do NOT assume TPC-H data is present (`SELECT count(*) FROM
lineitem` is the fast litmus test — currently errors "does not exist").

Gates run: `make ralph-state-guard` — same pre-existing stale
status/progress marker as recent loops, self-repaired, passed clean. No
`go build`/`go test` run (no Go/production code touched this loop).

In-flight: none. The throwaway crash-repro cluster (`/tmp/goopg-crash-repro`,
port 5533, cgroup units `goopg-crash-repro-{a..e}`) was fully stopped and its
directory removed before this loop ended. The shared `:65433` server is
still running (unchanged from last loop, still on the -0003g-fixed binary)
with only the 12 scratch tables in `tpch` — nothing else was left running or
modified on it.
