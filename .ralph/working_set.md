(idle — nothing in flight)

## Loop #16 result — M0134-0169 sized, engine-wide grammar fix landed, case PARKED

**Nightly triage:** `ci/logs/action-items.md` still run `20260828-235424` (unchanged
for a 2nd loop), both `## AI-` items already filed. Nothing new to file.

**Tree check first (loop #15's lesson):** `git status` mtimes vs the baton's — no
uncommitted engine WIP this time, baton `(idle)` was accurate. Selected per banner.

**Task:** M0134-0169 `sqljson_jsontable.sql`, sized live `not-tried` → `failed`
(1347 → **1335** diff lines, `^+ERROR` 116 → **115**), then PARKED.

The case is 100% blocked by the SQL/JSON `JSON_TABLE` subsystem (ledger 0168a,
which also gates -0170) — its 90 syntax errors reduce to four tokens: `COLUMNS`
x68, `PASSING` x12, `AS` x9 (all JSON_TABLE) and **`(` x1**, which was something
else entirely and is what the loop shipped.

**Shipped (engine-wide):** CTAS's source, `create_view_stmt` (both arms) and
`create_matview_stmt` took `select_bare`, so `CREATE TABLE t AS (SELECT 1)`,
`CREATE VIEW v AS (SELECT 1)` and `CREATE MATERIALIZED VIEW mv AS (SELECT 1)`
were rejected — all legal PG (`gram.y:4807`/`:4821`/`:11287` take `SelectStmt`).
Four productions `select_bare` → `SelectStmt`. Design
`docs/design/m0134-0169-ctas-view-source-parenthesised-query.md`.

**Three things worth carrying:**

1. **A guard is only as good as the premise that went in.** This bug survived
   the whole parser migration because it was recorded as *intentional* in THREE
   places — a `pg_grammar.y:634` comment asserting PG rejects `AS (SELECT 1)`,
   an `assertBothReject` entry, and two `!syntax error` goldens. All three were
   faithful records of the **legacy hand parser's** limit, promoted to a claim
   about PostgreSQL that nothing ever checked against `gram.y`. When a comment
   states a PG rule, verify it before trusting it — especially a comment that
   explains why goopg is *narrower*.
2. **`SelectStmt` is usable wherever gram.y uses it — conflicts stayed at 59.**
   The three-tier `SelectStmt`/`select_with_parens`/`select_no_parens` layering
   was built for exactly this; those call sites were leftovers, not a constraint.
   Cheap to check: edit, `make gen-parser`, read the pinned conflict count.
3. **Same-length regress diffs can still differ.** `join`/`subselect` moved
   `Values (431 rows)` → `(435 rows)` — goopg plans virtual `pg_class` as a
   Values node sized by the live relation count, and 4 relations that used to
   fail to parse now exist in the shared regress DB. Forward effect, not a plan
   regression. Also: **normalise the `---`/`+++` header paths before `cmp`**, or
   a worktree A/B reports every file as CHANGED.

Gates run: `make gen-parser` (59 conflicts, unchanged) ×4; `go build ./...` OK;
`go test ./internal/parser/` PASS; guard `TestCtasAndViewSourceAcceptsParenthesisedQuery`
**revert-checked** (fails on all ten with the grammar reverted); 15-case regress
A/B vs a HEAD worktree (11 byte-identical, `sqljson_jsontable` −12, `privileges`
−7, zero regressions); `RALPH_PRECOMMIT_SCOPE=units scripts/ralph-precommit-test.sh`
PASS; `scripts/tpch-spotcheck.sh` PASS (Q12 rows=2, Q13 rows=34);
`make check-testport-inventory` PASS; `make regen-testport` clean;
`make ralph-state-guard` OK (auto-repaired the stale completed marker).

In-flight: none.

**Carried obligations (14th loop):**
1. **TPC-DS SF0.5 gate still NOT run** (for -0156, -0157). -0158..-0169 are
   parser/DDL/catalog/ACL/wire/type-input-only and cannot move a TPC-DS plan.
   Nightly idle:
   `FORCE=1 GOOPG_BIN=$PWD/tmp/goopg-sf05-bin scripts/tpcds-sf05-regression.sh sweep`.
2. **The 110 "presumed stale, pending" 20260827 rows are STILL unadjudicated** —
   nightly testport keeps hitting the 120m timeout with no results.csv
   (`TestPort_IsolationSuite` full-run wedge, playbook §9).

NEXT LOOP (banner is the authority): M-NIGHTLY filing first, then
**M0134-0170 — `sqljson_queryfuncs.sql`** (status `not-tried`). 0168a gates it
too, so expect the same sizing-and-park unless the loop opens the SQL/JSON
grammar work.
