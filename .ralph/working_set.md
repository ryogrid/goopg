(idle — nothing in flight)

---

**Loop #24 (this loop) — COMPLETE, committed + pushed (`b9cfc369`, on
top of the peer's `b5d3e085`).**

Task: M0122-0004 `first_value`/`last_value`/`nth_value` window
functions — the previous loop's working_set explicitly named these as
the natural next slice on the same default-frame infra `sum`/`count`/
`avg`/`min`/`max` window aggregates landed on.

Files: `internal/planner/planner.go` (`buildWindowFunc` gained
`first_value`/`last_value` (1 arg) + `nth_value` (2 args) cases),
`internal/analyzer/{analyzer.go,analyzer_test.go}` (mirror
validation; `TestAnalyzeWindowFunctionUnsupportedRejected` repointed
at `ntile()` since `first_value()` is no longer a valid rejection
case), `internal/executor/{operators_window.go,window_compat_test.go}`,
`docs/design/{0020-0001-window-parser-and-ast.md,README.md}`,
`unimplemented_feat.json`, `.ralph/{fix_plan.md,deferral_ledger.md}`.

Key symbols: `hasFrameValueWindowFunc` gates a new per-partition
`frameEnd[]` array in `evalWindowFuncs` (`operators_window.go`),
derived from the *existing* `peerGroupBounds` — no new frame-bounds
computation needed since these three functions share the exact same
default frame (RANGE UNBOUNDED PRECEDING AND CURRENT ROW) the
aggregate window functions already use. `first_value` reads
`o.rows[pStart]` (frame head = partition start); `last_value` reads
`o.rows[frameEnd[localIdx]-1]` (current row's own peer-group tail);
`nth_value` evaluates its `n` argument per current row (same pattern
as `lag`/`lead`'s offset arg), rejects `n <= 0` with SQLSTATE `22016`
(matches `window_nth_value` in
`postgres/src/backend/utils/adt/windowfuncs.c` — including the exact
error text), returns `NULL` once `pStart+n-1` reaches/passes the
frame end. All three values cross-checked against a scratch upstream
PostgreSQL 18.3 instance (`postgres/local_install`), including the
`nth_value(val, 0)` error text — not assumed.

Gates run: `go build ./...` clean; `go test ./internal/parser/...
./internal/analyzer/... ./internal/planner/... ./internal/executor/...`
PASS (-count=1); scratch-PG cross-check (initdb+pg_ctl on a throwaway
port, torn down after); `scripts/tpch-spotcheck.sh` PASS (Q12=2/
Q13=33); pre-commit pgbench smoke PASS (0 failed, TPC-B ~228TPS/
simple-update ~245TPS/select-only ~13.5kTPS); `make ralph-state-guard`
auto-repaired a running/completed status skew (previous loop's clean
exit marker, not project completion), exit 0.

Concurrency note: peer `ralph_loop.sh` was live all loop. It committed
twice mid-loop (`2cacac14` M0119-0004 index-reloptions,
`b5d3e085` its own working_set carry) while I was mid-edit on
`.ralph/deferral_ledger.md` and `docs/design/README.md` — both files
I'd already appended/edited before the peer's `git add` swept the
current on-disk content (not just their own diff) into `2cacac14`.
Net effect: my ledger row and README fragment landed one commit early
(inside `2cacac14` instead of my own `b9cfc369`), but the *content* is
correct and intact — verified via `git show --numstat` before
proceeding, nothing was lost or corrupted. My own commit (`b9cfc369`)
was staged/committed via explicit `git commit -m ... -- <8 files>`
pathspec (deferral_ledger.md/README.md deliberately excluded from that
list since they were already in HEAD); `git show --stat b9cfc369`
confirmed exactly those 8 files. Hit one `.git/index.lock` collision
mid-loop (peer's own commit in flight) — waited ~10s for it to clear
rather than removing the lock, per the "never touch a peer's in-flight
git op" rule. Fetched (ahead-1/behind-0) then pushed clean
fast-forward.

Still open (recorded in ledger): `ntile`/`cume_dist`/`percent_rank` as
window functions (they exist only as `WITHIN GROUP` ordered-set
aggregates today — ordered-set semantics don't map onto
`peerGroupBounds`'s cumulative-frame model the way `first_value`/
`last_value`/`nth_value` did, so this is a distinct piece of work, not
a mechanical extension). ROWS/RANGE/GROUPS frame-clause parsing/
execution itself remains the largest open M0020 item, now with two
real consumers (`evalFrameAggFuncs` and `frameEnd`) to generalize
against. Combining forms (`OVER (win ORDER BY ...)`, named-window-
based-on-named-window) from earlier loops also remain open.

Next step: implement `ntile`/`cume_dist`/`percent_rank` as window
functions (needs its own semantics analysis — not frame-relative like
this loop's three), or ROWS/RANGE/GROUPS frame-clause parsing +
execution (parser already errors explicitly on them — see
`internal/parser/select.go`'s frame-clause reject), or pick up
M0122-0003's `pg_stat_io`/`track_io_timing` (architectural), or ledger
row 480's comma/LATERAL `ctx.OuterRows` gap. **Re-check `git status`
first** — the peer loop may have new WIP by the time the next loop
starts, and may be mid-`git commit` (wait for `.git/index.lock` to
clear rather than removing it).
