# SCOPE — TPC-DS dev gate migration: SF0.5 → SF0.25 (2026-09-11)

User order (binding, supersedes all earlier variants): after gate 5 + commits
land, migrate the development-time TPC-DS scale from 0.5-equivalent to
0.25-equivalent — cluster rebuild, golden replacement, AGENT.md etc.
procedure/script-name updates. Past analysis reports untouched.

## 1. Mechanical mapping (rename, not redesign)

The gate's architecture is unchanged (sampled TSVs → PG oracle fixture →
goopg sweep vs oracle). Every `sf05` becomes `sf025`, `SF05` becomes `SF025`:

| Old | New | Notes |
|---|---|---|
| `scripts/tpcds-sf05-regression.sh` | `scripts/tpcds-sf025-regression.sh` (`git mv`) | all `SF05_` vars, `sf05_` funcs, strings |
| `bench/tpcds/runtime_goopg/tpcds-data-sf05/` | `tpcds-data-sf025/` | sampling `% 2 == 0` → `% 4 == 0` (same shared-key parity argument, header rewritten) |
| `bench/tpcds/runtime_goopg/data-sf05/` | `data-sf025/` | fresh `load-goopg` |
| `bench/tpcds/runtime_goopg/tpcds-results-sf05/` | `tpcds-results-sf025/` | fresh `oracle` + `sweep` |
| PG db `tpcds05` | `tpcds025` | fresh `load-pg`; drop `tpcds05` after oracle lands |
| `tpcds-results-sf05/oracle.txt` (tracked) | `tpcds-results-sf025/oracle.txt` (tracked) | `git mv`-equivalent: `git rm` old + `git add` new = the golden 差し替え |
| port 65437 | **unchanged** | it is the fast-gate port; only its meaning changes SF0.5→SF0.25 |
| `goopg.sf05.log` (`SF05_LOG`) | `goopg.sf025.log` (`SF025_LOG`) | review note 1: the live log path moves too |
| CG scope `goopg-tpcds-sf05` | `goopg-tpcds-sf025` | |
| `server.sh [sf1\|sf05\|pg\|all]` | `[sf1\|sf025\|pg\|all]` | |
| `GOOPG_BIN=tmp/goopg-sf05-bin` (env example) | `tmp/goopg-sf025-bin` | review note 5: else the worked example recreates the old name |

Sampling note: `% 4 == 0` from the SF=1 TSVs (not re-halving the SF0.5 files —
one documented step, same parity-keeps-pairs proof). Dims stay whole.

## 2. Procedure/doc updates (the "AGENT.md等")

- `AGENT.md`: `SF=0.5 gate :65437` → `SF=0.25 gate :65437`.
- `CLAUDE.md`: port-table `65437` row + "fast regression gate" paragraph.
- `bench/tpcds/README.md`: full SF0.5 gate section → SF0.25 (names, dirs,
  db, example commands, env table, working-set figure re-measured).
- `bench/tpcds/timings/README.md`, `bench/tpcds/plans-pg/README.md`,
  `bench/tpch/plans-pg/README.md`: oracle/db-name mentions.
- `scripts/tpcds-bench-compare.sh`, `tpcds-sweep-diff.py`,
  `tpcds-plan-diff.py`, `tpcds-sf025-regression.sh` header,
  `scripts/planner-flags.sh`, `scripts/lib/bench-engine-id.sh`,
  `scripts/tpch-relsize-arm.sh`, `scripts/tpch-spotcheck.sh` comments.
- `scripts/estimate-parity-gate.sh`: `SF05_GOOPG_DATA` ref (`:37`, would
  break) + its `tmp/c20a/data-sf05` default name (`:36`) + the port-meaning
  comment at `:23-24` ("port 65437: those belong to the standing SF0.5
  gate" — review note 4).
- `docs/design/not_ralph/04-testing-and-gates.md`, `docs/design/not_ralph/TODO.md`
  P7.3 line: living procedure docs, updated. Everything else under
  `docs/`, `analysis/`, `ci/design/`, session files: historical, untouched.
- Memory: `tpcds_sf05_regression_gate.md` topic rewritten + `MEMORY.md`
  index line (rename, not duplicate).

## 3. Execution (all FOREGROUND)

1. `git mv` + sed rename + hand-fix comments/headers — explicitly
   including the oracle header template (old `:488` title line + `:490`
   dataset line: review note 6), which P0 adjudicates.
2. `build-data` (~20 s) → `load-pg` (COPY + ANALYZE) → `oracle` (~20 min
   PG capture) → `load-goopg` → `sweep` validation (~30 min expected),
   on a clean host with no `FORCE=1` (review note 8: the contamination
   guard must hold, or a contaminated oracle becomes the new golden).
3. PG cluster `:65438` must be running (`server.sh start pg`); goopg loads
   via the new script's own lifecycle (private by construction — fixed
   dirs/ports, same as the old gate).
4. After green: drop PG db `tpcds05`, `rm -rf` old `data-sf05/`,
   `tpcds-data-sf05/` (~2.8 GiB reclaim) + the orphaned
   `runtime_goopg/goopg.sf05.log` (review note 7: `SF025_LOG` moves the
   live path; the old file is dead). Archived sf05 sweep reports and
   plan captures (untracked history) stay on disk, untouched.
5. `git rm` old oracle, `git add` new oracle + renames; commit + push.

## 4. Falsifiable predictions

- P0: new oracle captures 99 data lines (13 header + 99 = 111 total, cf.
  review note 3); fact-driven query rows ≈ ½ of the sf05 oracle rows —
  EXCEPT ck=n/a LIMIT-window queries (e.g. `1|OK|100|n/a`, ~a dozen
  pinned at the 100-row bound), which will NOT halve; dim-only queries
  unchanged. Any `PG_ERROR` → diagnose (schema/COPY), not re-sample.
- P1: validation sweep exit 0 with PASS≈94 + Q72 TIMEOUT alone (the R64
  baseline set); any MISMATCH/CKMISMATCH/ERROR → STOP, adjudicate
  per-row (a re-sampled dataset legitimately moves row counts, but
  never goopg-vs-PG agreement on identical data). Expect the timeout
  set itself may need a baseline amendment (review note 9: Q4 already
  TIMEOUTs PG-side at SF0.5; quarter-scale can complete-or-timeout
  either side) — adjudicate, don't pre-fix.
- P2: `estimate-parity-gate.sh --help`-level smoke (it sources the env;
  a missed rename breaks it at source time).
- P3: `grep -rn "sf05\|SF05\|tpcds05\|data-sf05\|results-sf05\|SF0\.5\|SF=0\.5\|0\.5-equivalent"`
  over `scripts/ bench/tpcds/server.sh bench/tpcds/env_tpcds.sh
  bench/tpcds/README.md bench/tpcds/timings/README.md
  bench/tpcds/plans-pg/README.md bench/tpch/plans-pg/README.md
  AGENT.md CLAUDE.md` returns zero lines (review note 2: the bare
  `0\.5` pattern false-positives on `P0.5 base_yylex`, so it is scoped
  to scale-qualified forms; every live mention moves).

## 5. Gates

1. SCOPE review (this file), then implement.
2. P0–P2 above, all foreground.
3. REPORT.md (short: mapping, oracle capture stats, validation sweep
   summary, reclaimed disk), review, commit + push.
