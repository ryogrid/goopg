(idle — nothing in flight)

---

**Loop #18.** Concurrency guard confirmed a second, genuinely independent
`ralph_loop.sh` tree again this loop (root `2087326` → `2087655` →
`2462391`, separate from the screen-rooted `2085426` → `2085428` → `2462793`
tree). `git status` at loop start showed the peer's WIP still in progress
(`internal/catalog/catalog.go`/`codec.go`, `internal/executor/codec.go`/
`pg18_user_catalog_rows.go`(+test), `internal/initdb/open.go`/
`view_ddl_recovery_test.go`) — none of those files touched this loop.

Picked up `M0122-0004` (SQL language/executor backlog, ~21 items). Landed +
pushed `091fa948`:
- `internal/parser/token.go`/`keywords.go`: new reserved keywords
  `KwSymmetric`/`KwAsymmetric` (matches upstream kwlist.h's
  `RESERVED_KEYWORD` classification for both).
- `internal/parser/select.go`: new `p.acceptBetweenOrdering()` helper
  consumes the optional keyword after `[NOT] BETWEEN`; `parseBetweenTail`
  gained a `symmetric bool` param — `BETWEEN SYMMETRIC low AND high`
  desugars to `(x>=low AND x<=high) OR (x>=high AND x<=low)`, still
  entirely inside the parser (no analyzer/planner/executor change, same
  strategy as plain BETWEEN). `ASYMMETRIC` is an accepted no-op spelling.
- Tests: `internal/parser/between_test.go` (SYMMETRIC/NOT-SYMMETRIC/
  ASYMMETRIC AST-shape pins).
- **Verify-before-implement also closed a second stale bucket item**:
  "CTE-without-alias" (`unimplemented_feat.json` task `M0097-0029`) turned
  out already fixed by a same-day-in-history commit `8d281a1b` (synthetic
  `__sq_<pos>` alias for FROM-subqueries without an explicit alias) —
  confirmed via a throwaway probe test reproducing the uuid.sql shape the
  entry cited, then reverted the probe. No code change needed there, just
  closed the stale entry + fix_plan banner.
- Design: `docs/design/0003-0013-between-operator.md` new "Follow-up:
  BETWEEN SYMMETRIC/ASYMMETRIC" section (also removed the now-stale
  "Out of scope: BETWEEN SYMMETRIC" line); `docs/design/README.md` row
  updated in place. `.ralph/fix_plan.md` M0122-0004 banner updated (both
  sub-items struck from the open list, closure notes appended).
  `unimplemented_feat.json`: both matching entries (BETWEEN SYMMETRIC,
  CTE-without-alias) updated in place with `RESOLVED`/audit notes.
- Gates: `go build ./...` clean; `go test ./internal/parser/...
  ./internal/executor/... ./internal/planner/...` PASS (no regressions).
  Pre-commit pgbench smoke hook PASS (~97-136 TPS TPC-B, ~12.5k TPS
  select-only). No TPC-H spot-check needed — parser-only change, zero
  executor/planner/codec touch (per the practice card's own scoping: a
  BETWEEN desugar reuses existing BinaryOp/UnaryOp nodes, nothing new for
  the executor to evaluate). `make ralph-state-guard`: clean, no repair
  needed this loop.
- Committed via explicit `git add -- <9 files>` + `git commit -- <same 9
  files>`, verified `git show --stat HEAD` touched only those 9 files and
  the peer's dirty set was untouched before AND after. Pushed clean
  fast-forward (`0a1ddfe7..091fa948`).

**WITH-ORDINALITY named-column bug — ROOT CAUSE FOUND (background Explore
agent, completed after this loop's main task, not yet implemented):**

The 42703 is NOT in the planner (loop #17's own notes mis-pointed there —
`wrapOrdinality`/`planFromUnnest` are entirely correct and never even run
for the failing query). `planner.Plan()` calls `analyzer.Analyze()`
*before* `planSelect` (`internal/planner/planner.go:94-97`) and returns on
error, so the real bug is in **`internal/analyzer/analyzer.go`**:

- `lookupTable()` (analyzer.go:1471-1493) builds the FROM-item's synthetic
  table for any `TableFunc` via `tableFuncColumns(rv.TableFunc.Name, alias,
  rv.Columns)` — **no `WithOrdinality` param passed at all** (`grep
  WithOrdinality internal/analyzer/analyzer.go` = 0 hits).
- `tableFuncColumns()` (analyzer.go:1505-1611) hand-mirrors the planner's
  per-function dispatch but has no `"unnest"`/`"regexp_matches"` case;
  falls to `default:` (1603-1611) → **always a single column** named
  `colAliases[0]` (`"m"`). `"n"` is never added to this table, so naming it
  explicitly in the outer SELECT hits `lookupColumn` → 42703.
- `*` is unaffected only because `analyzeStar()` (817-837) does
  `return nil` immediately for an unqualified `*` (line 821-822) — no
  column-existence check at all; the real columns used downstream come
  from `planSelect`, which the analyzer never cross-checks.

**Fix**: thread `rv.TableFunc.WithOrdinality` into `tableFuncColumns`'s
signature (called from `lookupTable`), add real `unnest`/`regexp_matches`
element-type cases (currently silently wrong even without ordinality —
they return a generic `int8` column regardless of the SRF's actual output
type), and append a trailing ordinality column named `colAliases[len-1]`
when set — mirroring `wrapOrdinality`'s already-correct logic
(`internal/planner/planner.go:3426-3447`). This is planner-adjacent but
purely a parser/analyzer-package change (`internal/analyzer/`); check
whether that package is in the peer's dirty set before touching it.

Next step: re-check `git status` first (peer's catalog/executor/initdb WIP
may have landed by now; also confirm `internal/analyzer/` isn't part of
it). Then either implement the WITH-ORDINALITY analyzer fix above (small,
well-scoped, root cause fully diagnosed — just needs the
`tableFuncColumns` signature change + 2 new cases + ordinality-column
append), or continue M0122-0004's remaining open sub-items (window frames
/ GROUPING SETS / ANY-SOME-ALL / DEFAULT-clause / intervals — all still
open, none yet scoped) or the comma/LATERAL-join `ctx.OuterRows` wiring gap
(ledger row 480, separate from this one, still unscoped/unfixed).
