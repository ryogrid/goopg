(idle — nothing in flight)

## Loop #10 result — M0134-0164 PARKED, engine-wide relfilenode fix landed

**Nightly triage:** `ci/logs/action-items.md` still holds only run
`20260828-235424`'s two items; both already filed (fix_plan L1538, L1542).
Nothing new.

**Task: M0134-0164 (`sanity_check.sql`) — `not-tried` → `failed`, PARKED.**
77 → **21** diff lines standalone (129 → 21 inside the 13-case A/B, whose
baseline is higher only because that run loads all 13 cases' objects first —
both sides saw the identical load).

**Three things worth carrying:**

1. **`sanity_check.sql` is a pure INVARIANT PROBE, not a feature test.** Three
   statements, no schema of its own; in upstream's serial schedule it audits
   whatever ran before it. Its two queries are `pg_class` invariants a real
   cluster answers with zero rows, so every diff line is an engine-wide catalog
   defect by construction. Cheap to run, high signal — the same shape is worth
   looking for in other `_check`-style cases.
2. **Four pg_class row builders, four different answers.** `relfilenode` must be
   0 for storage-less relkinds (`RELKIND_HAS_STORAGE`, `pg_class.h:200`;
   `heap_create`, `heap.c:335-345`). The HEAP builder (`buildUserPGClassRow`)
   had an ad-hoc `p`/`v` check, initdb had the convention in comments
   (`relcache_init.go:770`, `initdb.go:6072`), the composite builder hardcoded 0
   — and BOTH VIRTUAL builders in `catalog.go` `PGClassRowsForDBOid` (table row
   AND index row) had none of it. Fixed with one shared
   `catalog.RelkindHasStorage` / `RelfilenodeForRelkind`. When touching a
   pg_class column, enumerate all four builders first.
3. **Row-literal formatting convention in `PGClassRowsForDBOid`:** hoist any
   multi-token cell to a local (as `relOfType` / `idxTablespace` already do).
   Inlining an expression re-aligns ~50 unrelated comment columns and churns the
   go1.25 gofmt baseline.

Gates run: 13-case regress A/B vs a HEAD worktree (`create_view`, `create_table`,
`alter_table`, `rules`, `dependency`, `inherit`, `matview`, `foreign_data`,
`sequence`, `indexing`, `create_index`, `psql`, `sanity_check`) — **10
byte-identical, ZERO regressions**; `alter_table` 3800 → 3798 as independent
confirmation. `create_index` is nondeterministic at byte level (Go pointer
address leaking into `pg_get_indexdef`, ledgered). New unit guard
`internal/executor/pg_class_relfilenode_storage_test.go`, revert-checked.
`RALPH_PRECOMMIT_SCOPE=units` PASS (exit 0). `scripts/tpch-spotcheck.sh` PASS
(Q12 rows=2, Q13 rows=34). pre-commit pgbench smoke PASS. Baseline worktree
removed.

In-flight: none.

**Carried obligations (9th loop):**
1. **TPC-DS SF0.5 gate still NOT run** (for -0156, -0157). -0158..-0164 are
   parser/DDL/catalog/ACL-only and cannot move a TPC-DS plan. Nightly idle:
   `FORCE=1 GOOPG_BIN=$PWD/tmp/goopg-sf05-bin scripts/tpcds-sf05-regression.sh sweep`.
2. **The 110 "presumed stale, pending" 20260827 rows are STILL unadjudicated** —
   nightly testport keeps hitting the 120m timeout with no results.csv
   (`TestPort_IsolationSuite` full-run wedge, playbook §9).

NEXT LOOP (banner is the authority): M-NIGHTLY filing first, then
**M0134-0165 — `security_label.sql`** (status `not-tried`). 0164a (pg_index has
no bootstrap catalog index rows) is a backlog follow-up, not the main sequence.
