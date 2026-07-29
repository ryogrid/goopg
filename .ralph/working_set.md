(idle — nothing in flight)

Last loop: **M0125-0018 COMPLETE** (committed + pushed; see git log).
Nightly triage done: `ci/logs/action-items.md` unchanged since 2026-07-25
(mtime Jul 25 03:20) — filing was a no-op again.

## NEXT: `M0125-0019` (executor — `string_agg(x, ',' ORDER BY x)` ignores its
own ORDER BY: goopg `3,1,2` vs PG `1,2,3`). The clause already SURVIVES parsing
(M0125-0009's `funcCallTailKey` keys on it), so the gap is purely that the
executor aggregate never sorts by it. Same wrong-answer-with-intact-row-count
blind-spot class as M0125-0009/-0010.

After that: **`M0125-0020`** (convert the set-op chain from a linked list to a
tree — retires `ParenBranches` + `InnerSegmentCount` + `InnerSortLimit`; it is
the real fix behind four deferral rows from -0006/-0017).

## Why not M0124 / M0125-0002..-0005 (do not re-derive — 4th loop unchanged)

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

- **The full 99-query SF0.5 gate is now 3 loops of debt.** Same reason each
  time: the sweep's ~21 GB Q5 peak does not fit beside the wedged 7.5 GB
  nightly server. A quiet-host loop should run it once.
- To decide whether a parser change can reach TPC-DS, scan the 99 query files
  directly (comment-strip + case-fold + a multiline regex over
  `bench/tpcds/runtime_goopg/tpcds-data/queries/*.sql`) — 10 s, and it gave
  M0125-0018 a definitive "zero hits". The heavier reflection-walk `main.go`
  trick is only needed when the shape is an AST FIELD, not source text.
- PG oracle for hand-written SQL: port **65438**, role **`ryo`** (NOT
  `postgres`), db `tpcds`, `psql -X -q -t -A` + temp tables.
- `internal/parser/select.go` is **already gofmt-dirty at HEAD** (go1.26 local
  vs go1.25 baseline) in `keywordUsableAsAlias`. Never `gofmt -w`; just confirm
  your own region is absent from `gofmt -d`.
- **Never `pkill -f`** — self-matches, kills the invoking shell (exit 144). Also
  `pgrep -af <word>` matches the Ralph `claude` process itself, because the
  prompt is on its command line.
- A `cd` inside a compound Bash command PERSISTS into later calls.

Gates run: units PASS; `tpch-spotcheck.sh` PASS (Q12=2, Q13=35); 36 new
by-value/AST tests PASS and **proved to FAIL at `74f4b264`** (14 executor + 10
parser subtests; all controls green there); gofmt clean on my hunks;
`make ralph-state-guard` OK (auto-repaired the stale completed marker);
pgbench smoke PASS via hook.
In-flight: none.
