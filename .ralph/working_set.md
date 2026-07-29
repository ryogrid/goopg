(idle — nothing in flight)

Last loop: **M0125-0003 stage 1 COMPLETE** at `c26c6fc3` (pushed). Flag-off and
inert; the fix_plan box stays UNCHECKED because stages 2–3 and the entire
four-arm measurement are still owed.

Nightly triage: `ci/logs/action-items.md` unchanged since 2026-07-25
(mtime Jul 25 03:20) — filing was a no-op again, fourth loop running.

## NEXT (banner order — `.ralph/fix_plan.md` "Current Priority" wins)

The user-set M0125 list (commit `d69fd834`) had stage 1 as item 1; it is now
done, so the next items are, in order:

1. **`M0124-0002`** — retroactive TPC-H + plan-baseline discharge. Produces
   `plan_snapshots/tpcds-round2-head.txt`, which M0125-0002/-0004/-0005 and
   stage 2 of -0003 all diff against and which still does not exist. **Needs a
   quiet host** (it is a timed A/B) — verify with `pgrep -af ci/batch`; only
   `nightly-scheduler.sh` should be present. Host was quiet all of this loop.
2. **`M0125-0012` (Q8)** — reproduces at SF0.5 in 12 s; still ERRORs in the
   sweep this loop ran (`query8.sql:10`).
3. **SF0.5 back half** — see In-flight below.
4. **`M0125-0014`/`-0015`** — pair with M0124-0002's quiet-host window.

M0125-0003 **stage 2** (bushy DP seed, `bushy.go:671`) is the natural follow-on
but is blocked on M0124-0002's snapshot: it moves plan SHAPE and needs a timed
22-query TPC-H run plus `make plan-diff LABEL=tpcds-round2-head`.

## Facts the next loop should NOT re-derive

- **`typeWidth` is now `get_typavgwidth`-faithful.** varchar(n) = n·4+4 then the
  sliding scale; char(n) is BPCHAR (full max width); numeric(p,s) via
  `numeric_maximum_size`. Do not "simplify" it back to `n+4`.
- **The relsize knob is a STAGE NUMBER**, not a bool: `GOOPG_RELSIZE_FALLBACK=2`
  enables consumers 1–2. Stage 2/3 consumers are unwired, so 2 and 3 behave as 1
  today. Test switch: `SetRelSizeFallbackStage`.
- **Planning is CONCURRENT** — goopg holds no lock around `planner.Plan`. Never
  add a package-global for per-plan state (`planParent` already is one; ledger
  row filed, out of scope).
- **PG oracle**: port **65438**, role `ryo`, db `tpcds`, `psql -X -q -t -A`.
  `bench/tpcds/server.sh start pg` (~15 s). Temp tables dodge autoanalyze and
  report `reltuples = -1` — that is how the four width/row anchors were taken.
  Stopped again at end of loop.
- **The SF0.5 sweep does not fit in one agent turn** (~1 h budget vs ~57 min
  usable). Run it as the loop's ONLY gate, or use `QUERIES=`.
- `go test ./internal/...` HANGS in `internal/testport`. Use
  `RALPH_PRECOMMIT_SCOPE=units scripts/ralph-precommit-test.sh`.
- gofmt-dirty at HEAD (never `gofmt -w`): `catalog.go` 27 hunks,
  `planner/planner.go` 5 — this loop added **zero** new drift (verified by
  stashing the edit and re-counting).
- **Never `pkill -f`** — self-matches, kills the invoking shell (exit 144).

Gates run: units PASS; `tpch-spotcheck.sh` PASS (Q12=2 rows, Q13=35 rows);
TPC-DS SF0.5 sweep reached **Q53/99 with ZERO status changes** vs
`sweep-20260729-123114.txt` before being cut; planner package PASS;
PG-18.3 oracle match exact on 4 relations; pgbench smoke via commit hook;
`make ralph-state-guard` OK.

In-flight: none. (The SF0.5 sweep was terminated deliberately at 3400 s —
`timeout 3400 scripts/tpcds-sf05-regression.sh sweep`, output
`bench/tpcds/runtime_goopg/tpcds-results-sf05/sweep-20260729-181319.txt`,
covered Q1–Q53. It cleaned up its own servers: `pgrep -af 'bin/goopg'` was
empty afterwards. Q54–Q99 is unmeasured debt, ledger row 2026-07-29 —
resume with `QUERIES="$(seq 54 99)"`, do NOT re-run from Q1.)
