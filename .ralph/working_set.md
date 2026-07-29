(idle — nothing in flight)

Last loop: **M0125-0020 COMPLETE** — the set-op chain is now a TREE.
Nightly triage done: `ci/logs/action-items.md` unchanged since 2026-07-25
(mtime Jul 25 03:20) — filing was a no-op again.

## ⚠ STATE CHANGE: THE NIGHTLY WEDGE IS GONE — the host is QUIET

`run-nightly.sh` (PID 2511542) and the 7.5 GB `goopg-bench-bin` (PID 2621153)
are **no longer running**; only `nightly-scheduler.sh` (2230863) remains.
`free -g` showed 27 GB available. This unblocks, for the first time in six
loops, the two items that needed a quiet host. Re-verify with
`pgrep -af ci/batch` before selecting.

## NEXT (banner order — `.ralph/fix_plan.md` "Current Priority" wins)

The banner puts **M0124 → M0125 → M-NIGHTLY**. M0124 is now selectable:

1. **`M0124-0002`** — retroactive TPC-H + plan-baseline discharge (a TIMED A/B;
   needs the quiet host, which now exists). Produces
   `plan_snapshots/tpcds-round2-head.txt`, which `M0125-0002/-0004/-0005` all
   diff against and which still does not exist.
2. **`M0124-0004`** — Q35's row count (two previous readings voided by the
   nightly batch; harness guards refuse to start while it runs, `FORCE=1`
   overrides and is legitimate for row-count work, never for a timing).
3. Also owed: the **FULL 99-query SF0.5 gate** (`scripts/tpcds-sf05-regression.sh
   sweep`, ~1 h, no `QUERIES=`), 7 loops of debt, diff against
   `tpcds-results-sf05/sweep-20260729-123114.txt` (PASS=75 MISMATCH=1
   CKMISMATCH=3 ERROR=2 TIMEOUT=14 SKIP=4). Ledger row 2026-07-29.

Remaining M0125 items are blocked as before: -0007/-0008 wait for the sweep to
reach Q99; -0002/-0004/-0005 need M0124-0002's snapshot; -0003 needs a four-arm
timed study.

## Facts the next loop should NOT re-derive

- **TPC-DS reachability grep** (10 s, decisive): for set-op shapes, only Q14,
  Q23, Q87 have a parenthesised operand followed by a set-operator. Q87/Q23
  checksums to match: `b363a9287bdd0920` / `00f53003bda23764`.
- SF0.5 **subset probe**: `QUERIES="14 23 87 …" scripts/tpcds-sf05-regression.sh
  sweep` — stamped "SUBSET PROBE", NOT a gate result. ~20 min for 10 queries.
- **HEAD-worktree proof** (~2 min, cheap): `git worktree add /tmp/X HEAD
  --detach`, copy the new test file in, `cd /tmp/X && go test -run …`. The shell
  cwd resets automatically afterwards. Removes with `git worktree remove --force`.
- PG oracle: port **65438**, role **`ryo`**, db `tpcds`, `psql -X -q -t -A`.
  It was DOWN at loop start — `bench/tpcds/server.sh start pg`.
- Throwaway goopg (~40 s): `./bin/goopg init -D tmp/X -N` then `GOOPG_CG_UNIT=n
  nohup scripts/goopg-test-run.sh ./bin/goopg start -D tmp/X --listen
  127.0.0.1:5533 &`; `./bin/goopg stop -D tmp/X`. Subcommand is `init`, not
  `initdb`.
- Already gofmt-dirty at HEAD (never `gofmt -w`): `catalog.go` 27 hunks,
  `parser/ast.go` 8, `planner/planner.go` 5, `parser/select.go` 1.
- `go test ./internal/...` HANGS in `internal/testport` (10 min panic). Use
  `RALPH_PRECOMMIT_SCOPE=units scripts/ralph-precommit-test.sh`.
- **Never `pkill -f`** — self-matches, kills the invoking shell (exit 144).

Gates run: units PASS; `tpch-spotcheck.sh` PASS (Q12=2 rows, Q13=35 rows);
SF0.5 subset probe PASS=6 ck-verified / MISMATCH=0 / ERROR=0 (Q5/Q14/Q54
TIMEOUT, identical to baseline); 27-statement psql matrix byte-identical vs
PG 18.3; 13 new by-value subtests PASS (5 proved failing at `8ce216dd`);
gofmt hunk counts unchanged; pgbench smoke via commit hook;
`make ralph-state-guard` — see status block.
In-flight: none.
