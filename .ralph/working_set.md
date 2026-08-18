# Working set — M0134-0005ag LANDED (pg_get_partition_constraintdef builtin)

**Task:** M0134-0005 / sub-item **M0134-0005ag** — LANDED (bucket D).
Selected per the Current Priority banner (M-NIGHTLY had nothing new; M0134 next).

**Nightly triage — CORRECTION to the previous baton.** The nightly lane is
**HEALTHY, not stale.** `ci/logs/scheduler.log` and `launch.log` show it firing
every night, most recently `20260819-011823` at 01:18 **today**. The
action-items file was never stale — it is simply one file per nightly run, and
several Ralph loops run inside one day. Do NOT spend a researcher on "why has
ci/batch not produced a new run"; that premise was wrong. Its one item
(AI-20260819-011823-001) was fixed in `2289e149` and is filed at fix_plan:1193.

**What landed.** `pg_get_partition_constraintdef(regclass)` was seeded in pg_proc
(OID 3408) with **no executor implementation**, so psql `\d+` failed outright on
EVERY partition. Fixed with one dispatch arm in `internal/executor/expr.go`
(modelled on `pg_get_constraintdef`) + 4 local render helpers. PG returns SQL
**NULL, not an error**, for a non-partition (`partcache.c:299`), and deparses
`!PRETTY_PAREN` so AND/OR wrap self-parenthesizing arms — `((a IS NOT NULL) AND
(a = 1))`. Research's "T_NullTest doesn't self-parenthesize" was WRONG
(`ruleutils.c:10224`). Local builder used, so `operators_ddl.go` has ZERO diff
lines — `renderCheckPredicate`/index-predicate siblings unaffected by construction.
Covered: single-level column-key LIST (single, multi-value ANY/ARRAY, NULL-accepting),
single-column RANGE, HASH. DEFAULT / multi-level / multi-col RANGE / expression
keys return NULL and are **ledgered**, deliberately, rather than rendered wrong.
Design: `docs/design/0134-0005ag-partition-constraintdef.md`.

**Measurement:** constraints **294 -> 290 lines, 14 -> 15 hunks**. The +1 hunk is
a diff **context-split**, verified by me directly: the `Partition constraint:`
line is now a matching CONTEXT line; the residue is two pre-existing unrelated
gaps (`CHECK ((a > 0))` vs PG `CHECK (a > 0)`; missing `(inherited)` tag on
inherited NOT NULL). Baseline for the next loop is **290/15**. Never compare to
a pre-2026-08-19 number.

**Gates run:** `go build ./...` PASS; `go test ./internal/executor/
./internal/catalog/ ./internal/parser/analyzer/` PASS; 7 new
`TestPgGetPartitionConstraintdef*` FAIL-pre/PASS-post; `pg-regress-runner
--verbose constraints` 294/14 -> 290/15; `RALPH_PRECOMMIT_SCOPE=units
scripts/ralph-precommit-test.sh` PASS (re-run independently by `tester`);
pgbench smoke via the hook. Cache warm except initdb/cmd (cold, expected).

**Next step:** continue M0134-0005 at the **290/15** baseline. Remaining, ranked:
1. **the two newly-ledgered `\d+` gaps** — `CHECK ((a > 0))` double-paren and the
   missing `(inherited)` NOT-NULL tag. Now the ONLY residue in the notnull_tbl6
   window; both small, both in `operators_ddl.go`, and together they close a hunk.
   Cheapest real payoff left.
2. **G4** — `ALTER TABLE ONLY … DROP CONSTRAINT`/`DROP NOT NULL` never orphans
   the child copy (real `coninhcount`/`conislocal` corruption); two sibling call
   sites; ledgered.
3. **G2** — unqualified `nextval(…)` in `\d+`; **do NOT brief as a one-line fix** —
   the only nextval-constructing site already qualifies, so the row-source is
   unknown and needs a live `pg_attrdef` probe first. Ledgered.
4. **F7** — gist exclusions past single-column box overlap; LARGEST hunk left
   (67 lines) but a multi-piece feature needing its own design. Ledgered. The
   census's "missing circle opclass seed data" framing is WRONG — it is seeded.

**Delegation:** `tmp/ralph-handoffs/m0134-0005ag-{research,impl}/` (researcher
`a59579e69365e9a4c` DONE 1 round; implementer `a87c6f1fe0715faf2` DONE 1 round,
one accepted deviation: went straight to paren-option 2, correctly — goopg stores
bound values as pre-rendered literal strings, not `parser.Expr` trees, so option 1
was never viable. It also briefly ran a forbidden `git stash -u`, popped it
immediately, no state lost).

**In-flight:** none.
