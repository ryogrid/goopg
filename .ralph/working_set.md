(idle — nothing in flight)

Last loop: **M0125-0019 COMPLETE** (committed + pushed; see git log).
Nightly triage done: `ci/logs/action-items.md` unchanged since 2026-07-25
(mtime Jul 25 03:20) — filing was a no-op again.

## NEXT: `M0125-0020` (convert the set-op chain from a linked list to a TREE —
retires `ParenBranches` + `InnerSegmentCount` + `InnerSortLimit`; it is the real
fix behind four deferral rows from -0006/-0017). Large parser+planner change;
accept by VALUE with the whole existing -0006/-0016/-0017/-0018 matrices as
non-regression.

Also newly filed and cheap: **`M0125-0021`** (a `bytea` literal is carried as
escaped TEXT — `length('\xaabb'::bytea)`=6 vs PG 2, `encode(<bytea>,…)` returns
`''` for hex/base64/escape. `encode` returning `''` instead of erroring makes it
a SILENT wrong answer). Resume in `internal/executor/expr.go` at the `::bytea`
cast.

## Why not M0124 / M0125-0002..-0005 (do not re-derive — 5th loop unchanged)

- `M0124-0002` / `M0124-0004` need a QUIET host. **The nightly wedge is STILL
  there** (below) → unselectable.
- `M0125-0004`, `-0002`, `-0005` diff against `plan_snapshots/
  tpcds-round2-head.txt`, which is M0124-0002's deliverable and does not exist.
  `M0125-0003` needs a four-arm TIMED study → host blocker.

## ⚠ STILL BLOCKED ON THE USER — the nightly wedge is UNCHANGED (now ~14h)

Run `20260729-002344` wedged since ~02:07; `goopg-bench-bin` PID **2621153**
still resident (7.5 GB RSS). `kill` of non-owned PIDs is hard-denied by the
classifier, so I cannot clear it.

```
kill -TERM 2511542    # run-nightly.sh   — stops before stage-tpcds
kill -QUIT 2621153    # goopg server     — QUIT, not KILL: untrapped, so it
                      # dumps the leaked backend's stack to
                      # ci/logs/20260729-002344/tpch/server.log
```
Re-check with `pgrep -af ci/batch`.

## Facts the next loop should NOT re-derive

- **The full 99-query SF0.5 gate is now 4 loops of debt.** Same reason each
  time: the sweep's ~21 GB Q5 peak does not fit beside the wedged 7.5 GB
  nightly server. A quiet-host loop should run it once.
- `internal/executor/` has an IN-PROCESS SQL harness — `newDDLFixture(t)`
  returns `(ctx, _, cleanup)` and `runQuery(t, ctx, sql)` returns `[]Row`.
  No server, no port, ~10 ms per matrix. Use it for by-value acceptance.
- To check whether a change can reach TPC-DS, grep the 99 query files directly
  (`bench/tpcds/runtime_goopg/tpcds-data/queries/*.sql`) — 10 s. It gave both
  -0018 and -0019 a definitive "zero hits".
- PG oracle for hand-written SQL: port **65438**, role **`ryo`** (NOT
  `postgres`), db `tpcds`, `psql -X -q -t -A`.
- `internal/executor/operators_join_agg.go` is **already gofmt-dirty at HEAD**
  (12 hunks, go1.26 local vs go1.25 baseline) in the joinOp struct, the
  `floatSpecialKind` consts, and ~8 more spots. Never `gofmt -w`; verify your
  own hunks are absent from `gofmt -d` and that the hunk COUNT is unchanged.
- **Never `pkill -f`** — self-matches, kills the invoking shell (exit 144).
- A `cd` inside a compound Bash command PERSISTS into later calls.

Gates run: units PASS; `tpch-spotcheck.sh` PASS (Q12=2 rows, Q13=35 rows);
17 by-value subtests PASS and **13 proved to FAIL at `6088e41b`** with all
three controls green there; gofmt hunk count unchanged (12→12);
`make ralph-state-guard` OK (auto-repaired the stale completed marker);
pgbench smoke PASS via hook.
In-flight: none.
