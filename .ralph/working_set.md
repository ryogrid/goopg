(idle — nothing in flight)

---

**Loop #40 (this session) — COMPLETE, committed + pushed (`c128093c`).**

Task: M0122-0004 — implement the `{ROWS|RANGE|GROUPS} BETWEEN ... AND
... [EXCLUDE ...]` window frame clause. Previously the parser rejected
any frame clause outright and every window function (sum/count/avg/
min/max/first_value/last_value/nth_value/etc.) hard-coded PostgreSQL's
*default* frame — this was the sole remaining open item in the M0020/
M0122-0004 window-function series per the prior loop's own note.

Landed: `ROWS` mode end-to-end (`RANGE`/`GROUPS` parse structurally but
are rejected `0A000` — a deliberate, ledgered v0 scope limit, not a
bug: they need value-based peer comparison / group-counting bounds,
a materially separate model from `ROWS`' row-index arithmetic).
Parser: new `parser.WindowFrame`/`FrameMode`/`FrameBoundKind`/
`FrameExclusion` AST (`internal/parser/expr.go`), `parseFrameClause`/
`parseFrameBound`/`parseFrameExclusion` (`internal/parser/select.go`)
mirroring gram.y's `opt_frame_clause`/`frame_extent`/`frame_bound` —
all new keywords (ROWS/RANGE/GROUPS/UNBOUNDED/PRECEDING/FOLLOWING/
CURRENT/EXCLUDE/TIES/OTHERS) are soft ident-keywords, no lexer change.
Analyzer: `validateWindowFrame` (`internal/analyzer/analyzer.go`)
reproduces gram.y's bound-ordering checks (`42P20`) and rejects
`RANGE`/`GROUPS` (`0A000`). Planner: `WindowAgg.Frame` (`internal/
planner/plan.go`/`planner.go`), `resolveWindowFrame` resolves offset
exprs against the window's input schema; `windowSpecKey` now hashes
the frame too (`windowFrameKey`) so differing frames don't collapse
into one node. Executor (`internal/executor/operators_window.go`):
`frameBounds` reproduces `nodeWindowAgg.c`'s `update_frameheadpos`/
`update_frametailpos` ROWS arithmetic exactly (clamped, out-of-order →
empty frame); `resolveFrameOffset` evaluates offsets once per query
like `limitOp` (`22004`/`42804`/`22013`); `evalExplicitFrameAggFuncs`
recomputes sum/count/avg/min/max per row (frame can shrink, no valid
running total — correctness-first, TPC-H doesn't use frame clauses so
this doesn't touch the spot-check gate); `frameRowExcluded`/
`firstInFrame`/`lastInFrame`/`nthInFrame` implement `EXCLUDE CURRENT
ROW`/`GROUP`/`TIES`; `first_value`/`last_value`/`nth_value` switch to
`frameBounds` under an explicit frame; `cume_dist` deliberately stays
frame-independent (matches `window_cume_dist` — never consults frame).
All new logic is gated behind `o.plan.Frame != nil`, so the pre-existing
default-frame path (and all its tests) is untouched byte-for-byte — the
scoping report's hazard didn't materialize.

Tests: `internal/parser/window_test.go` (7 new frame-shape tests +
repointed the old reject-test to accept), `internal/analyzer/
analyzer_test.go` (`TestAnalyzeWindowFrameRowsAccepted`/
`RangeGroupsRejected`/`BoundOrderingRejected`), `internal/executor/
window_compat_test.go` (`TestCompatWindowExplicitRowsFrameSliding`/
`ExcludeCurrentRow`/`ExcludeGroupAndTies`/`FrameNegativeOffsetRejected`
— all cross-checked row-for-row against a scratch upstream PostgreSQL
18.3 instance spun up on port 5546 for this verification). Design:
`docs/design/0020-0001-window-parser-and-ast.md` new Follow-up section;
`docs/design/README.md` row extended; both matching
`unimplemented_feat.json` entries annotated in place (surgical edit,
not a full rewrite); `.ralph/deferral_ledger.md` row appended
(2026-07-05) for the remaining `RANGE`/`GROUPS` gap with a concrete
resume point (`validateWindowFrame`'s mode check + a `frameBounds`
RANGE/GROUPS branch). Committed as `c128093c` (14 files, pathspec-
scoped to stay disjoint from the peer's in-flight `pgstat_io.go`/
`pgstat_io_test.go`/`bufpool.go`/`bufpool_counters_test.go`/
`initdb/open.go`/`docs/design/0122-0003-explain-format-xml-yaml.md`),
pushed to `origin/align-data-structure-with-pg`.

Note on `working_set.md`/ledger/fix_plan/unimplemented_feat.json/design
doc content: this loop's context was compacted mid-session (a very long
single turn implementing 4 subsystems); the bookkeeping edits to these
shared files were made earlier in the same turn and had already landed
on disk by the time the summarized context resumed — verified their
content against `git diff` before committing rather than re-deriving.

Next step for a future M0122-0004 loop: `RANGE`/`GROUPS` frame modes
(`internal/analyzer/analyzer.go`'s `validateWindowFrame`'s
`fr.Mode != parser.FrameModeRows` early-reject; `internal/executor/
operators_window.go`'s `frameBounds` needs a RANGE branch that
binary-searches the ORDER BY column's value ± offset instead of row
counts, and a GROUPS branch counting peer-group boundaries via the
existing `peerGroupBounds`). Combining named-window forms (`OVER (win
ORDER BY ...)`) and intervals (sub-day units) remain separately
deferred per the M0122-0004 fix_plan bucket. Re-check `git status` +
`pgrep -af ralph_loop.sh` fresh at loop start — a peer loop was active
throughout this one (confirmed via `pgrep`/`git status` polling before
staging; its own M0122-0003 `write_time`/evictions/extends work stayed
untouched by this commit).

Gates: `go build ./...` clean; `go vet`/`go test -count=1
./internal/parser/... ./internal/analyzer/... ./internal/planner/...
./internal/executor/... ./internal/storage/...` PASS (no regressions,
including all pre-existing default-frame window compat tests
byte-identical); `scripts/tpch-spotcheck.sh` PASS (Q12=2/Q13=33,
elapsed 28.08s/92.07s) — two earlier attempts hit host-load-induced
startup/connection instability (2 concurrent ralph loops + this
session on one machine, 9GB swap in use) unrelated to this diff (Q13
doesn't touch window functions); a clean re-run passed cleanly. Mandatory
pre-commit pgbench smoke hook PASS (188/249/14237 TPS across TPC-B/
update/select-only). `make ralph-state-guard` PASS (one auto-repair:
`progress.json`'s stale `completed` marker from a prior loop's clean
exit reconciled to `in_progress`, unrelated to this loop's own files).
