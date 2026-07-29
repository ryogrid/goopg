(idle — nothing in flight)

Last loop: **the full 99-query TPC-DS SF0.5 gate (banner item 4) RAN and PASSED.**
Eight loops of debt discharged. Artifact `analysis/tpcds-sf05-full-gate-20260729/`
(README = method + the eight-diff table; `merged-sweep-head-50cf7c5f.txt` = 99 rows).
Banner item 4 struck through with the result; ledger row appended; M0125-0007
un-blocked.

Nightly triage: `ci/logs/action-items.md` unchanged since 2026-07-25 (mtime Jul 25
03:20); all 26 `AI-` subjects already filed under M-NIGHTLY — no-op again.

## NEXT (banner order — `.ralph/fix_plan.md` "Current Priority" wins)

Banner items 1–4 are now all done. Remaining:

1. **`M0125-0014`/`-0015`** (Q49/Q51 **SF=1** re-measure) — banner item 5. Both now
   PASS at SF0.5, but SF0.5 is a key-parity half-sample; take the SF=1 reading
   before ticking. Quiet host.
2. **`M0124-0004`** (Q35 row count) — the last open M0124 item; quiet host. Check
   `ci/batch/run-nightly.sh` is not running first (harness guards refuse).
3. **`M0125-0007`** (unpadded month/day date decode) — now unblocked, and the gate
   handed it an acceptance signal (see below). Codec change ⇒ needs
   `tpch-spotcheck.sh` + SF0.5 gate + the FULL regress-port suite (Rule #5).

## Facts the next loop should NOT re-derive

- **Gate result at HEAD `50cf7c5f`**: `PASS=75 (46 ck-verified) MISMATCH=1
  CKMISMATCH=3 ERROR=1 TIMEOUT=15 SKIP=4`. Timeout class = Q5 Q8 Q10 Q14 Q30 Q31
  Q35 Q54 Q64 Q65 Q67 Q69 Q71 Q78 Q81 (**15**, not the 17 M0125 is named after).
  Only ERROR is Q75 (M0125-0004); only row MISMATCH is Q47.
- **Q16/Q94/Q95 share ONE goopg checksum `512b5fdab820c47b`** (the `0/NULL/NULL`
  answer) vs three different oracle cks. One defect = M0125-0007, three queries.
  Acceptance: that single ck must become three distinct oracle-matching values.
- **Q72/Q88 TIMEOUT→PASS is a threshold artefact, not a fix** — they finish at
  263s/236s; the 2026-07-27 baseline capped at 180s, today's default is 300s.
- **Do NOT re-open Q8 as a regression**: ERROR→TIMEOUT is M0125-0012 working.
- **How to run the gate in one loop**: four contiguous `QUERIES="$(seq A B)"`
  chunks on ONE pre-built binary (`go build -o tmp/goopg-bench-bin ./cmd/goopg`
  — the script only builds if the file is MISSING, so rebuild explicitly or you
  measure a stale binary). Splits used: 1-30 (44m), 31-53 (19m), 54-72 (44m),
  73-99 (22m); ~110 min total. Each chunk is stamped "SUBSET PROBE"; the merged
  file is the gate result. Ledger row carries the caveat + a `RESUME_FROM` fix.
- `pgrep -f '<pattern>'` **self-matches the invoking Bash shell**; use `pgrep -x`
  / `ps -C`. (Confirmed again: `ps -C goopg-bench-bin` cleanly showed no orphan.)

Gates run: the full 99-query SF0.5 gate (above) — the loop's deliverable;
`make ralph-state-guard` OK; pgbench smoke via the commit hook. No code changed,
so no unit/spotcheck/plan-diff run was owed.

In-flight: none.
