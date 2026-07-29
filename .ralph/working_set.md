(idle — nothing in flight)

Last loop: **M0125-0017 COMPLETE** (committed + pushed; see git log).
Nightly triage done: `ci/logs/action-items.md` unchanged since 2026-07-25
(mtime Jul 25 03:20) — filing was a no-op again.

## NEXT: `M0125-0018` (parser-only, cheapest remaining accepted-by-value item)

`x IN ((A) EXCEPT (B))` and `EXISTS ((A) EXCEPT (B))` both raise a syntax
error; PG accepts (`a_expr IN_P select_with_parens`, `EXISTS
select_with_parens`). Resume point is already in fix_plan: both operand parsers
assume the token after `(` is the SELECT keyword, so route them through
`parseParenthesisedSelectStmt` the way the scalar-subquery path
(`internal/parser/select.go:2862`) already does. Derived-table / CTE / scalar
contexts already work and are pinned by M0125-0006's tests.

After that: `M0125-0019` (executor, `string_agg(… ORDER BY …)` ignored) and the
newly filed **`M0125-0020`** (convert the set-op chain from a linked list to a
tree — retires `ParenBranches` + `InnerSegmentCount` + `InnerSortLimit`).

## Why not M0124 / M0125-0002..-0005 (do not re-derive — 3rd loop unchanged)

- `M0124-0002` / `M0124-0004` need a QUIET host. **The nightly wedge is STILL
  there** (below) → unselectable.
- `M0125-0004`, `-0002`, `-0005` diff against `plan_snapshots/
  tpcds-round2-head.txt`, which is M0124-0002's deliverable and does not exist.
  `M0125-0003` needs a four-arm TIMED study → host blocker.

## ⚠ STILL BLOCKED ON THE USER — the nightly wedge is UNCHANGED (now ~13h47m)

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

- **The full 99-query SF0.5 gate is now 2 loops of debt.** Same reason both
  times: the sweep's ~21 GB Q5 peak does not fit beside the wedged 7.5 GB
  nightly server. A quiet-host loop should run it once (same wait as M0124-0002).
- `QUERIES="…" scripts/tpcds-sf05-regression.sh sweep` is a **subset probe**;
  needs `FORCE=1` while the nightly runs. Q23/Q38/Q49/Q87 are the usable set-op
  queries (Q8 ERROR + Q14 TIMEOUT are pre-existing — skip them, Q14 burns 300 s).
- **To decide whether a parser/planner change can reach TPC-DS at all**, drop a
  `main.go` under `internal/parser/<tmpdir>/` (internal-package import rules
  forbid `/tmp`) that parses all 99 query files and reflection-walks every
  `SelectStmt`. M0125-0017 got a definitive "zero hits" in ~1 min this way.
  query36/70/86 fail to parse for unrelated pre-existing reasons.
- PG oracle for hand-written SQL: port **65438**, role **`ryo`** (NOT `postgres`
  — that role does not exist), `psql -X -q -t -A` + temp tables.
- `internal/parser/{ast,select}.go` and `planner.go` are **already gofmt-dirty at
  HEAD** (go1.26 local vs go1.25 baseline): ast.go 8 hunks, select.go 1,
  planner.go 5. Never `gofmt -w`; just confirm your own region is absent.
- **Never `pkill -f`** — self-matches, kills the invoking shell (exit 144).
- A `cd` inside a compound Bash command PERSISTS; use `go test -C <dir>`.

Gates run: units PASS; `tpch-spotcheck.sh` PASS (Q12=2, Q13=35); SF0.5 set-op
subset PASS (4/4 checksum-verified, byte-identical to `sweep-20260729-123114`);
27 new by-value/structural tests PASS and **proved to FAIL at `19d844b4`**
(8 executor subtests + both planner invariants; all controls green there);
gofmt clean on my hunks; pgbench smoke PASS via hook.
In-flight: none.
