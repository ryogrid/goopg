## Loop summary — M0134-0160 (`reloptions.sql`)

**Nightly triage:** all 133 `AI-20260827-052222-*` items already filed in
fix_plan; no new ids. Nightly `20260828-235424` finished its TPC-DS sweep
(1711s) at 03:17.

Task: **M0134-0160 — `reloptions.sql`** — PARKED (`not-tried` → `failed`,
232 → **201** lines, `^+ERROR` 17 → **6**). Shipped fix is **engine-wide**:
goopg validated storage parameters only by *recognising* them, so any
`WITH (...)` name nobody looked for was silently accepted and dropped —
`CREATE TABLE t(i int) WITH (not_existing_option=2)` SUCCEEDED, as did the
bad-namespace / CREATE INDEX / ALTER TABLE SET forms. PG raises 22023
(`parseRelOptions` `reloptions.c:1488`, `transformRelOptions` `:1275`).

Files: NEW `internal/executor/reloptions_catalog.go` (+ `_test.go`);
`internal/executor/operators_ddl.go` (6 call sites);
`internal/parser/ast.go` + `support.go` (`CreateIndexStmt.WithOptionNames`);
`internal/parser/testdata/parity_goldens.txt` (37 rows, purely additive);
3 existing tests corrected. Design
`docs/design/m0134-0160-reloption-name-registry.md` + README index.

Key symbols: `relOptKind`, `relOptionKinds`, `relOptionNamespaces`,
`indexRelOptKind`, `validateRelOptionNames/Map`, `execCreateTable`,
`execCreateIndex`, `execAlterTableSetReloptions`.

**Three things worth carrying:**
1. `acceptOidsOff` (`reloptions.c:1307-1322`) is NOT trivia — a first cut
   without it broke `CREATE TEMP TABLE withoutoid() WITH (oids = false)`.
   Only the A/B sweep caught it (`create_table` 609 → 622).
2. `index_reloptions()` runs BEFORE `index_create()`'s name-conflict test,
   so the check must sit before the "already exists" check.
3. **Two existing tests pinned non-PG behavior** — `buffering` is GiST-only,
   `fastupdate` is GIN-only. Verified against a live PG 18.3 oracle
   (`initdb` to /tmp, port 15499) before changing them. Do that first
   whenever a "regression" is an existing goopg test disagreeing with a
   PG-faithful change.

Gates run: 14-case regress A/B vs HEAP worktree `/tmp/goopg-relopt-base` —
`reloptions` 232→201, `alter_table` 3792→3784, **twelve byte-identical**,
ZERO regressions (`create_index`'s only delta is a pre-existing
nondeterministic Go pointer address in `pg_get_indexdef`).
`go test ./internal/executor/` + `./internal/parser/` PASS.
`RALPH_PRECOMMIT_SCOPE=units` PASS. `scripts/tpch-spotcheck.sh` PASS
(Q12 rows=2, Q13 rows=34; ran with a private `GOOPG_BIN`).
`make regen-testport` / `check-testport-inventory` PASS.
`make ralph-state-guard` OK (auto-repaired the previous loop's marker).

In-flight: none. Worktree `/tmp/goopg-relopt-base` still present — remove with
`git worktree remove --force /tmp/goopg-relopt-base`. Probe PG stopped/removed.

**Carried obligations (5th loop):**
1. **TPC-DS SF0.5 gate still NOT run** (for -0156, -0157). -0158/-0159/-0160
   are parser/DDL-only and cannot move a TPC-DS plan. Nightly is now idle:
   `FORCE=1 GOOPG_BIN=$PWD/tmp/goopg-sf05-bin scripts/tpcds-sf05-regression.sh sweep`.
2. **The 110 "presumed stale, pending" 20260827 rows are STILL unadjudicated** —
   nightly testport keeps hitting the 120m timeout with no results.csv (the
   `TestPort_IsolationSuite` full-run wedge, playbook §9). File when a `## AI-`
   item for it appears.

NEXT LOOP (banner is the authority): M-NIGHTLY filing first, then
**M0134-0161 — `replica_identity.sql`** (status `not-tried`).
0160a/0160b are backlog follow-ups, not the main sequence.
