(idle — nothing in flight)

Last loop: **`M0125-0014` and `M0125-0015` both CLOSED** — Q49/Q51 re-measured at
SF=1 on HEAD `f3f31d87`, both value-equal to PG. Artefacts
`analysis/m0125-0014-0015-q49-q51-sf1/`; design doc § Q49 / § Q51 "Execution
record"; ledger `tpcds-round2 Q49` → resolved, `q47-q49-q51` UPDATEd (stays open
for Q47); banner item 5 discharged.

Nightly triage: `ci/logs/action-items.md` still unchanged (mtime Jul 25 03:20),
all 26 `AI-` subjects already filed — no-op. Separately FILED (not worked, per
banner): the TPC-DS nightly row-anchor mechanism is dead.

## NEXT (banner order — all five ordered items are now discharged)

1. **`M0125-0007`** (unpadded month/day date decode) — the banner's new
   fall-back selection. One defect behind the three Q16/Q94/Q95 `CKMISMATCH`
   cells; it has a fresh acceptance signal waiting. **Codec change ⇒ full bar
   INCLUDING the regress-port suite** (Rule #5), plus `tpch-spotcheck.sh` and the
   SF0.5 gate.
2. **`M0125-0013`** (Q47) — the only member of the Q47/Q49/Q51 document still
   actionable.

## Facts the next loop should NOT re-derive

- **Q49 = `OK 83 s / 34 rows`, `ck=63ace0d888e86982`; Q51 = `OK 47 s / 100 rows`,
  `ck=443e242cfab22c02`** at SF=1, both equal to PG's checksum. The
  `OK 79 s / 30` and `OK 587 s / 0` sweep rows are superseded — do not re-measure.
- **Q51 is no longer budget-marginal**: 553 s of headroom, not 13 s. Q82
  (`OK 556 s`, 44 s) is again the sweep's narrowest `OK` margin.
- **Neither mechanism was ever found.** Both closed at STEP 0 (the M0124-0004
  "resolve *or classify*" shape). Attribution to M0125-0009 is inherited from the
  SF0.5 bisect, NOT measured at SF=1 — one reading at HEAD cannot separate -0009
  from -0010/-0011/-0012/-0020. Don't restate it as measured.
- **The TPC-DS nightly row anchors have never worked**: `summarize.py:485` reads
  `r["rows"]`, the CSV column is `expected_rows` ⇒ all 63 dropped. TPC-H is fine
  (its CSV uses `rows`). Filed under M-NIGHTLY, parked; the Q49/Q51 anchors added
  this loop are inert until it lands.
- `scripts/tpcds-result-checksum.py` takes a **path argument**, not stdin.

Gates run: `make ralph-state-guard` (INCONSISTENT → auto-REPAIRED → OK); pgbench
smoke via the commit hook. No engine code changed (measurement + docs + CI
metadata only), so the units/spotcheck/SF0.5/plan-diff bar was not owed.

In-flight: none.
