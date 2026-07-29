(idle — nothing in flight)

Last loop: **`M0125-0007` CLOSED** — PG-faithful date/time field decode.
`d_date = '2002-5-01'` matched zero rows and raised nothing; three TPC-DS
queries answered `0 / NULL / NULL` because of it. New leaf package
`internal/pgdatetime` (`NormalizeInput`) + every executor entry point.
Artefacts `analysis/m0125-0007/`; design
`docs/design/0125-0007-pg-faithful-date-field-decode.md` (indexed); 5 ledger
rows; new item **M0125-0023** filed for Q95.

Nightly triage: `ci/logs/action-items.md` unchanged (mtime Jul 25 03:20), all
26 `AI-` subjects already filed — no-op.

## NEXT (banner order, rewritten this loop)

1. **`M0125-0008`** — owns TWO of the three CKMISMATCH cells now: Q94 AND Q16.
2. **`M0125-0013`** (Q47).
3. **`M0125-0023`** (Q95) — newly filed.
Owed independently: **one full 99-query SF0.5 gate run on a quiet host.**

## Facts the next loop should NOT re-derive

- Q16/Q94/Q95 at SF0.5, post-fix vs PG: Q16 `63|319602.45|-91294.46` vs
  `23|93334.17|-35323.69`; Q94 `7|10534.30|7178.64` vs `2|5037.18|1067.82`;
  Q95 `5|11180.00|-6205.20` vs `23|45031.03|-1282.36`. goopg cks
  `863c4e96d8930d66` / `fb2c619e9bcb6bae` / `663cec31dac6449c`. **Q16/Q94
  over-count (= M0125-0008); Q95 under-counts and has NO `EXISTS` (= -0023).**
- The one shared wrong-answer ck `512b5fdab820c47b` is GONE. Do not re-probe.
- **Pre-existing, NOT introduced** (both reproduce at `337526b1` padded):
  `'0002-01-01'::date` → `1755-08-30` (Go `time.Time` ns range; `Datum.Int`
  holds UnixNano, PG holds a day count); `'…03:04:05.25-04'::timestamp` →
  `07:04:05.25` (plain `timestamp` must DISCARD the offset). Ledger row filed.
- `d_date = 'garbage'` is STILL silently false (PG raises 22007) — deliberate
  deferral, own ledger row; `promoteCrossKind` has one caller (`compareDatum`).
- PG oracle cluster :65438 takes user **`ryo`**, not `postgres` (goopg takes
  `postgres`). `bench/tpcds/server.sh {start|stop} {pg|sf05}`.
- `pg-regress-runner.sh --all` HANGS; the quick set (52) + the 6 datetime
  suites take ~40 s each and are the usable form. Its per-test `.diff` files
  embed absolute paths and a timestamp header — strip lines 1-2 AND the repo
  path before comparing two trees, or all 52 look changed.
- A worktree off HEAD needs `ln -s <main>/postgres <wt>/postgres`; `postgres/`
  is a real untracked dir in the main tree, not a symlink.

Gates run: units suite PASS; `tpch-spotcheck.sh` PASS (Q12=2, Q13=35);
regress-port quick set + 6 datetime suites diffed vs a HEAD-built worktree
binary — 1/52 PASS on both, all diffs identical bar a clock-dependent `uuidv7`
test; SF0.5 **3-query value probe only** (`FORCE=1`, nightly CI batch owned the
host — full gate owed, ledger row); `make ralph-state-guard`; pgbench smoke via
the commit hook.

In-flight: none.
