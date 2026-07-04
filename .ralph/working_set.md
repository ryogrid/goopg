(idle — nothing in flight)

---

**Loop #19 (this loop) — COMPLETE, committed + pushed (`97e7318c`).**
Picked up the M0122-0004 backlog item "ANY/SOME/ALL" (confirmed via a
background research agent + my own code reading that it was genuinely
incomplete, not stale — unlike window frames/GROUPING SETS which are
medium/large and GROUPING SETS/DEFAULT-clause/intervals which are
already largely done or too big for one loop).

Landed: `expr op ANY|SOME|ALL (array | subquery)` now works for every
comparison operator (`=`,`!=`,`<>`,`<`,`>`,`<=`,`>=`) and all four POSIX
regex operators. Previously only `=`/`!=`/`<>`/regex supported ANY
(array/scalar only), `ALL` worked only for `=` (via a NOT-wrap hack),
`SOME` wasn't a recognized keyword at all, and no operator accepted a
`(SELECT ...)` subquery operand.

- `internal/parser/token.go`/`keywords.go`: new `KwSome` keyword
  (unreserved, same class as the pre-existing `KwAny`).
- `internal/parser/expr.go` / `internal/planner/plan.go`: `InExpr` gains
  `AllOp bool` (AND semantics) beside the pre-existing `AnyOp` (element-
  wise operator, OR semantics). Threaded through all planner construction
  sites (`planner.go:5787,9513,10497,10726`, `foldconst.go:66`).
  `internal/executor/expr.go`'s `evalInExpr` gained the ALL branch
  (AND-short-circuit-false), placed before the existing ANY branch.
- `internal/parser/select.go`: `parseAnyTail` now also accepts a `SELECT`
  operand (mirrors `parseInTail`'s subquery detection) — previously only
  `ARRAY[...]`/scalar. New `isAnyOrSomeTok`/`isAllTok` helpers. Extended
  the pre-existing regex-ANY block and the `=`/`!=`/`<>` ANY block in
  place to accept SOME/ALL; added ONE new dispatch block for the
  previously-uncovered combos (`<,>,<=,>=` × ANY/SOME/ALL, `!=`/`<>` ALL).
  Did NOT rewrite the pre-existing `= ALL` NotEqualAny/NOT-wrap path —
  left it untouched to avoid regressing already-shipped behavior.
- Subquery operand needed **zero new executor plumbing**: discovered
  `collectInValues` (`internal/executor/expr.go`) already generically
  drains an arbitrary single-column subquery plan (built for
  `IN (subquery)`) and is read identically regardless of List vs Plan —
  confirmed via `planInExpr` (`internal/planner/planner.go:9503`) already
  threading `AnyOp` through both branches unconditionally.
- Tests: `internal/parser/any_all_test.go` (AST-shape pins for all
  operator×quantifier combos + subquery form), `internal/executor/any_all_test.go`
  (end-to-end array + subquery evaluation, incl. `v > ALL (SELECT ...)`
  shapes). Both new files, no existing tests modified.
- Design: `docs/design/0003-0008-subqueries.md` new "Follow-up: ANY / SOME
  / ALL" section (removed the doc's own stale "ANY/SOME/ALL... deferred"
  out-of-scope line); `docs/design/README.md` row updated in place.
  `.ralph/fix_plan.md` M0122-0004 banner updated (ANY/SOME/ALL struck from
  open list, closure note appended). `unimplemented_feat.json`'s matching
  entry ("ANY/SOME/ALL quantified subqueries") updated in place with
  RESOLVED audit note. `.ralph/deferral_ledger.md` new row recording the
  one deliberate known limitation below.
- **Known limitation (recorded, not fixed):** NULL-element handling is
  not fully three-valued — both ANY and ALL skip NULL elements and
  return a definite true/false rather than propagating NULL per upstream
  `ScalarArrayOpExpr` semantics. This predates this loop (the ANY branch,
  M0097-0068); the new ALL branch deliberately mirrors the same
  simplification for consistency rather than being asymmetrically "more
  correct". Resume point in the ledger if a failing test ever demands it.
- Gates: `go build ./...` clean; `go test ./internal/parser/...
  ./internal/planner/... ./internal/executor/...` PASS (no regressions,
  confirmed via a retry-loop around a transient build break — see
  concurrency note below). Pre-commit pgbench smoke hook PASS (0 failed
  transactions; ~227-247 TPS TPC-B/simple-update, ~14.1k TPS select-only).
  `make ralph-state-guard`: found status/progress inconsistent
  (status="running" vs progress="completed" from a prior loop's clean-exit
  marker), auto-repaired to progress="in_progress", now OK.

**Concurrency note:** the peer `ralph_loop.sh` tree (screen-rooted
`2085426` chain, live PID `2652067` this loop) was actively writing to
`internal/catalog/catalog.go`/`codec.go`, `internal/executor/codec.go`/
`operators_storage.go`/`pg18_user_catalog_rows*.go`/`storage_dml_test.go`,
`internal/initdb/open.go`/`view_ddl_recovery_test.go`,
`docs/design/0110-0001-pg-dump-tap-port.md` throughout this loop — none of
those files were touched. Mid-loop the peer's edit to
`operators_storage.go` (calling a not-yet-defined `dmlPrivilegePermitted`)
transiently broke `go test ./internal/executor/...` (NOT `go build ./...`,
which doesn't compile `_test.go` files) for ~1 poll cycle; a retry-loop
(`until go test ...; do sleep 5; done`) confirmed it self-resolved once the
peer finished that edit — no action needed on my end beyond not panicking
at a red build that isn't mine. Committed via explicit `git add -- <15
files>` + `git commit -m ... -- <same 15 files>` (message BEFORE `--`,
not after — `git commit -- <paths> -m msg` mis-parses the message as a
pathspec), verified `git show --stat HEAD` touched only those 15 files
and the peer's dirty set was byte-identical before and after. Fetched
+ pushed clean fast-forward (`129f7be9..97e7318c`, ahead-1/behind-0 at
fetch time).

Next step: pick M0122-0004's remaining open sub-items — window frames
ROWS/RANGE/GROUPS (medium: needs new `WindowFrame` AST field + executor
frame evaluation, `parseWindowDef` currently hard-errors on any frame
clause, `internal/parser/select.go:3437`), GROUPING SETS/ROLLUP/CUBE
(large: parser already accepts the syntax but the planner comment at
`internal/planner/planner.go:5070-5072` says explicitly it's "handled as
plain GROUP BY on the key columns" — no real multi-level grouping-set
expansion, no NULL-substituted rows, likely no `GROUPING()` function;
needs a probe to confirm `GROUPING()` status before scoping), or DEFAULT-
clause/intervals (both already extensively implemented per this loop's
research agent — would need a probe to find any real remaining gap before
picking). Also still open: the comma/LATERAL-join `ctx.OuterRows` wiring
gap (ledger row 480), and M0122-0003's `pg_stat_io`/`track_io_timing`
remainder. Re-check `git status` first — the peer's dirty file set above
may have changed by the time the next loop starts.
