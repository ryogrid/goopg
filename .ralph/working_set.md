(idle — nothing in flight)

Last loop: **M0125-0016 COMPLETE**, committed + pushed as `9e75cfaa`.
Nightly triage done: `ci/logs/action-items.md` unchanged since 2026-07-25
(mtime Jul 25 03:20), all items already filed — filing was a no-op.

## Why M0125-0016 and not M0124 / M0125-0002..-0005 (do not re-derive)

Same blockers as the previous loop, re-verified:
- `M0124-0002` / `M0124-0004` need a QUIET host. **The nightly wedge is STILL
  there** (below), so both stay unselectable.
- `M0125-0004`, `-0002`, `-0005` diff against
  `plan_snapshots/tpcds-round2-head.txt`, which is M0124-0002's deliverable and
  **does not exist**. `M0125-0003` needs a four-arm TIMED study → host blocker.
- `M0125-0016` was the topmost item accepted **by value**, so host contention
  cannot void it.

**NEXT: `M0125-0017`** (topmost unchecked, accepted by value, parser+planner
only — no host needed). Its resume point is already written in fix_plan:
M0125-0006's `ParenBranches` is exactly 1 for the broken single-branch case and
is the natural carrier; consume it at `planner.go`'s `innerBoundary`. Note that
`innerBoundary` is now ALSO a precedence barrier (M0125-0016) — any change there
must keep both roles. `M0125-0018` (parser-only) is the cheaper alternative.

## ⚠ STILL BLOCKED ON THE USER — the nightly wedge is UNCHANGED

Run `20260729-002344` wedged since ~02:07; `goopg-bench-bin` PID **2621153**
still resident (7.5 GB RSS, now ~13 h elapsed). `kill` of non-owned PIDs is
hard-denied by the classifier, so I cannot clear it. Until it is gone,
**M0124-0002 must not be selected**.

```
kill -TERM 2511542    # run-nightly.sh   — stops before stage-tpcds
kill -QUIT 2621153    # goopg server     — QUIT, not KILL: untrapped, so it
                      # dumps the leaked backend's stack to
                      # ci/logs/20260729-002344/tpch/server.log
```
Re-check with `pgrep -af ci/batch`.

## Debt this loop deliberately left

**The full 99-query SF0.5 gate was NOT run for M0125-0016.** Its guard refuses
under the live nightly (`FORCE=1` would be legitimate — this gate is accepted by
row count/checksum, never timing — but the sweep's ~21 GB Q5 peak does not fit
beside the wedged 7.5 GB nightly server in an 18 GB budget). Ran the set-op
subset instead: `FORCE=1 QUERIES="8 14 23 38 49 87" scripts/tpcds-sf05-regression.sh sweep`
→ Q23/Q38/Q49/Q87 checksums **byte-identical** to HEAD sweep
`sweep-20260729-123114.txt`; Q8 ERROR + Q14 TIMEOUT pre-existing, unchanged.
A quiet-host loop should run the full gate once (same wait as M0124-0002).

## Facts the next loop should NOT re-derive

- **`QUERIES="…" scripts/tpcds-sf05-regression.sh sweep` is a SUBSET PROBE mode**
  — cheap, and the report labels itself as not-a-gate-result. Needs `FORCE=1`
  while the nightly runs.
- **TPC-DS cannot reach a set-op precedence bug**: Q8/Q14/Q38 are the only
  queries containing INTERSECT and every chain is homogeneous (hence
  associative). Q8's INTERSECT is at query8.sql:91.
- `planner.go` is **already gofmt-dirty at HEAD** (go1.26 local vs go1.25
  baseline); its hunks are at lines 2317–5649. Never `gofmt -w` it — check only
  that your own region is clean.
- **Never `pkill -f`** — it self-matches and kills the invoking shell (exit 144).
- A `cd` inside a compound Bash command PERSISTS to later calls; use
  `go test -C <dir>` for worktree runs instead.
- The "prove the gate fails first" recipe that worked twice now:
  `git worktree add /tmp/<x> HEAD --detach`, copy the new test file in,
  `go test -C /tmp/<x> -run <Test> ./internal/executor/`, then
  `git worktree remove --force`.

Gates run: units PASS; `tpch-spotcheck.sh` PASS (Q12=2, Q13=35); SF0.5 set-op
subset PASS (4 checksum-identical, 2 pre-existing failures unchanged); 17 new
by-value tests PASS and **proved to FAIL at `70e1ca61`** (8 subtests, all
controls + the whole barrier suite passing there); M0125-0006's matrix
unchanged; gofmt clean on my hunks; pgbench smoke PASS via hook
(653 / 689 / 13843 tps).
In-flight: none.
