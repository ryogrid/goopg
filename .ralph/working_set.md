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

---

**Loop #17 (this loop).** Concurrency guard again confirmed a second,
independent `ralph_loop.sh` tree (screen-rooted `2085426` chain, this
session rooted at `2087326`→`2087655`). Rather than block, found an
existing but stale, uncommitted worktree at `/tmp/wt-buffers-dirtied-written`
(branch `explain-buffers-dirtied-written`, only 2 commits behind current
HEAD) that a prior loop iteration had left mid-flight on exactly the next
open `M0122-0003` sub-item (`dirtied=`/`written=` BUFFERS counters) — per
`worktree_isolation_escapes_foreign_wip_block`, rebased it onto current
HEAD (clean, no conflicts on the code; one `docs/design/README.md` merge
conflict from `git stash pop`, resolved by keeping the newer upstream
`0122-0002` row + the stash's extended `0122-0003` row with the
dirtied/written closure paragraph), fixed one gofmt alignment nit in my
own added `nodeStats` struct lines (did NOT touch the pre-existing
unrelated gofmt drift in the same files — repo is on a stale gofmt
version, `goopg_gofmt_version_mismatch_no_w`), verified
`go build ./...`/`go vet`/`go test ./internal/executor/...
./internal/storage/...` all clean, appended a new deferral-ledger row
(closing the dirtied/written gap named by the two BUFFERS rows above it),
committed (`e49ac798`), rebased again onto 2 newer peer commits
(`091fa948`/`5d5c5ec1` landed mid-loop), rebuilt/retested clean, pushed
fast-forward (`5d5c5ec1..a896a842`), then removed the worktree + deleted
the local branch. Pre-commit pgbench smoke PASS (0 failed, TPC-B ~189
TPS / simple-update ~254 TPS / select-only ~14.4k TPS via `PATH`+
`LD_LIBRARY_PATH` pointed at `postgres/local_install/{bin,lib}` — the
worktree has no `postgres/` dir of its own, it's untracked in the main
tree). Did **not** run `scripts/tpch-spotcheck.sh` for real: the worktree
has no `bench/tpch/runtime_goopg/data` (untracked, main-tree-only, and
was actively being touched — mtime during this loop — by the concurrent
peer's own possible spotcheck run); script SKIPPED cleanly (exit 0, by
design for a machine/worktree without the data set). Risk judged low: this
change is purely additive instrumentation (new atomic counters + new
EXPLAIN render fields), touches no join/filter/row-count logic, and a
prior loop iteration's design-doc text already recorded a real-server
live verification of this exact code (`UPDATE` immediately after `INSERT`
withholds `dirtied=`, reports it after an intervening `CHECKPOINT`).

M0122-0003 remaining open items (see the ledger's newest row): `EXPLAIN
(BUFFERS)` without `ANALYZE` (no planning-phase buffer counters exist),
local/temp-buffer terms (no local-buffer-manager concept at all), the
other 7 `pg_stat_io` I/O counters, `track_io_timing` runtime `SET`.

Next step: (idle for this thread — task fully committed+pushed). Good next
picks per the fix_plan banner: continue M0122-0003's remaining sub-items
above (all need the same "storage-instrumentation-layer" work, flagged
multi-loop), or M0122-0004's open items (see Loop #18's note above), or
next `M0119-0004` pg_dump catalog-view parity slice. **Before picking
anything touching `internal/catalog/`, `internal/executor/codec.go`,
`internal/executor/pg18_user_catalog_rows*.go`, `internal/initdb/open.go`,
`internal/initdb/view_ddl_recovery_test.go`, or `internal/analyzer/` — those
were the concurrent peer's live dirty set as of this loop's end; re-check
`git status` first.**
