# M0146-0014 — census at 80c8249f9 (2026-11-04, run-2 loop #52): exhausted tree=684bcd0a54a09435c9163abe690e8b67f1b8586b

Trigger: `scripts/s9-census-due.sh` returned DUE (base routing-20261104d, reason=plan-relevant-change)
— code commit `80c8249f9` (M0148-0001f3a: a string literal passed to a text parameter of a built-in
function prints as `'…'::text`; `internal/catalog/builtin_proc_signatures.go`,
`internal/executor/operators_explain.go`). Its own fire-set gate measured `fires=Q15` on TPC-H only.
`git diff --quiet -- internal cmd scripts bench go.mod go.sum` held while the capture built its
engines, hence `tree=` above (HEAD's tree). Capture repo head `406e379a4` (records on top of the
code commit).

Capture: `BASELINE_REV=456b0527b CORPORA="tpcds-sf025 tpcds-sf1 tpch" scripts/tpcds-fireset-gate.sh s9bb tmp/census-20261104e`
(BASELINE_REV = the head consumed by routing-20261104d; candidate engine `tmp/fireset-bin/s9bb-candidate`
sha256 `19352a059d865069c545c64b5d79c21199639700506311bf51bf349c9d4efb8f`).

| corpus | capture | match | `--compare` vs BASE |
|---|---|---|---|
| TPC-H | `s9bb-tpch*` | 22 / 22 | CHANGED (new 62dae406…, base 5175c597… routing-20261102f) |
| TPC-DS SF0.25 | `s9bb-tpcds-sf025*` | 99 / 99 | IDENTICAL (53e4d570…) |
| TPC-DS SF1 | `s9bb-tpcds-sf1*` | 99 / 99 | IDENTICAL (5b26baf8…) |

The TPC-H change is expression text only, one line of Q15 (the fire-set plan A/B `fires=Q15`): the
lineitem filter now prints `to_date('1996-01-01'::text, 'YYYY-MM-DD'::text)`, PG 18.3's own
rendering in `s9bb-tpch-pg.plans.txt`. The `'3 month'::interval` literal still differs from PG's
`'3 mons'::interval` (canonical output text of a non-text literal; filed under M0148-0001f3 item 3).
No plan shape or cost changed (PLAN-PARITY 22/22).

CATEGORIES-EXCL-MATCH is 0 in every category on every corpus. No task is filed. Because a plan text
changed, the full capture is committed (S9(4a)); this directory becomes the TPC-H BASE. Item 10 is
live: M-NIGHTLY, then the M0148 queue.
