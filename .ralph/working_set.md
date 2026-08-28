## Loop summary — M0134-0161 (`replica_identity.sql`)

**Nightly triage:** run `20260828-235424` (sha `5773b884c`) had 2 items, both
already-known subjects; filed a traceability section in fix_plan. AI-…-001
(`TestSyntax_AdvisoryLock_…BeginBoundary`) is ALREADY FIXED in `b479ebfd4`,
which lands *after* that run's sha — the run predates the fix, not a
re-regression. AI-…-002 is a dup of the open `tpch/Q5-timeout` row.

Task: **M0134-0161 — `replica_identity.sql`** — PARKED (`not-tried` →
`failed`, 194 → **189** lines, `^-ERROR` 3 → **2**). Shipped fix is
**engine-wide**: `pg_index.indimmediate` is keyed on the `DEFERRABLE` flag
ALONE (`index.c:1049`, `index.c:2080-2082`), never on INITIALLY DEFERRED,
and SEVEN goopg consumers had drifted into THREE different answers. The two
wrong validation paths silently ACCEPTED a `UNIQUE (b) DEFERRABLE` index as
both a replica identity and an ON CONFLICT arbiter; PG rejects both.

Files: `internal/catalog/catalog.go` (new `(*Index).IsImmediate()` + the
virtual pg_index builder); `internal/executor/pg18_user_catalog_rows.go`
(heap pg_index builder); `internal/executor/operators_ddl.go`
(`resolveReplicaIdentityIndex`); `internal/parser/analyzer/analyzer.go` +
`internal/optimizer/planner.go` (both ON CONFLICT arbiter branches — the
inferred-by-column one had NO check at all). Tests: NEW
`internal/executor/replica_identity_indimmediate_test.go`,
`internal/optimizer/with_test.go`. Design
`docs/design/m0134-0161-indimmediate-deferrable-key.md` + README index.

Key symbols: `Index.IsImmediate`, `resolveReplicaIdentityIndex`,
`resolveArbiterIndex`, `analyzeOnConflict`, `uniqueCheckDeferred`.

**Three things worth carrying:**
1. `uniqueCheckDeferred` (`deferred_unique.go:40`) had the rule RIGHT, with
   the upstream citation, while five siblings had it wrong. A correct,
   well-commented sibling is not evidence the codebase agrees — grep every
   consumer of a catalog column before trusting any one of them.
2. Ordering is load-bearing in the inferred-by-column arbiter branch: the
   `!IsImmediate()` check must run AFTER inference matching, never as a
   `continue` filter. PG's `infer_arbiter_indexes` deliberately does not
   filter on indimmediate (`plancat.c:817`) and lets the executor raise
   (`execIndexing.c:604-610`); filtering during matching yields 42P10 where
   PG reports 55000.
3. A `git worktree` for an A/B baseline needs the untracked `postgres`
   symlink recreated (`ln -s $(readlink -f postgres) <wt>/postgres`) or
   `make gen-parser` dies on `kwlist.h`. The worktree also materialises an
   EMPTY `postgres/` dir first — `rmdir` it before `ln -s`, else the link
   lands inside it. Build with `make -C` / `go build -C`, never `cd`.

Gates run: 13-case regress A/B vs HEAD worktree `/tmp/goopg-indimm-base` —
`replica_identity` 194→189, **twelve byte-identical**, ZERO regressions
(`create_index`'s only delta is the pre-existing nondeterministic Go pointer
address in `pg_get_indexdef`). `RALPH_PRECOMMIT_SCOPE=units` PASS (EXIT=0).
`scripts/tpch-spotcheck.sh` PASS (Q12 rows=2, Q13 rows=34, private
`GOOPG_BIN`). `make regen-testport` / `check-testport-inventory` PASS.
`make ralph-state-guard` OK (auto-repaired the previous loop's marker).

In-flight: none. Baseline worktree removed; probe PG (port 15499) stopped
and its datadir deleted.

**Carried obligations (6th loop):**
1. **TPC-DS SF0.5 gate still NOT run** (for -0156, -0157). -0158..-0161 are
   parser/DDL/catalog-only and cannot move a TPC-DS plan. Nightly is idle:
   `FORCE=1 GOOPG_BIN=$PWD/tmp/goopg-sf05-bin scripts/tpcds-sf05-regression.sh sweep`.
2. **The 110 "presumed stale, pending" 20260827 rows are STILL unadjudicated** —
   nightly testport keeps hitting the 120m timeout with no results.csv (the
   `TestPort_IsolationSuite` full-run wedge, playbook §9). File when a `## AI-`
   item for it appears.

NEXT LOOP (banner is the authority): M-NIGHTLY filing first, then
**M0134-0162 — `roleattributes.sql`** (status `not-tried`).
0161a-0161h are backlog follow-ups, not the main sequence.
