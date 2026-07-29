(idle — nothing in flight)

Last loop: **`M0124-0004` CLOSED on the classify branch — M0124 is now fully
closed.** Artefacts `analysis/tpcds-q35-m0124-0004/`; design doc §"Execution
record (2026-07-30)"; ledger row 2026-07-30; banner updated.

Nightly triage: `ci/logs/action-items.md` unchanged since 2026-07-25 (mtime
Jul 25 03:20); all 26 `AI-` subjects already filed under M-NIGHTLY — no-op.

## NEXT (banner order — `.ralph/fix_plan.md` "Current Priority" wins)

M0124 has no open items. M0125's ordered list items 1–4 are done. Next:

1. **`M0125-0014`/`-0015`** (Q49/Q51 **SF=1** re-measure) — banner item 5. Both
   PASS at SF0.5, but SF0.5 is a key-parity half-sample; take the SF=1 reading
   before ticking. Quiet host.
2. **`M0125-0007`** (unpadded month/day date decode) — unblocked; the gate handed
   it an acceptance signal. Codec change ⇒ needs `tpch-spotcheck.sh` + SF0.5 gate
   + the FULL regress-port suite (Rule #5).

## Facts the next loop should NOT re-derive

- **Q35 is settled: performance-only, RC-8 shape.** Outer cardinality
  (`customer ⋈ customer_address ⋈ customer_demographics`) = **96,562**; one
  buffer-**warm** `EXISTS` #1 floors at **8.16 s** (×4, ±0.5 %) ⇒ the AND-ed
  conjunct alone floors at **≈9.1 days** at SF=1, ≈4.6 days at SF0.5. Do **not**
  re-run Q35 at a bigger budget — 3600 s is still ~3 orders of magnitude short.
  It is now **M0125-0003's acceptance query** (first terminating run vs oracle
  `35|OK|100|0`).
- **The 525 s SF=1 history is REFUTED**, not unreproduced — it is an artefact of
  the PATH-loss event that also ate the row count. `651 s`/`628 s` are kill
  lines. Q35 has never completed on goopg at any scale factor.
- **The SF0.5-slower-than-SF=1 anomaly is closed** — dimensions are copied whole
  so outer cardinality is unchanged; both SFs are kill lines whose ordering is
  noise. Nothing left to explain.
- **`c26c6fc3` (M0125-0003 stage 1) is confirmed shape-neutral**: SF=1 `EXPLAIN`
  at HEAD is byte-identical to the pre-stage-1 capture.
- `timeout N psql` leaves the **server** executing — the SF=1 probe's server was
  still alive at 30:30 etime after the client died; `server.sh stop sf1` reaped
  it. `ps -C <exe>` (not `pgrep -f`) to check, to avoid the self-match.

Gates run: `make ralph-state-guard` (INCONSISTENT → auto-REPAIRED → OK);
pgbench smoke via the commit hook. No code changed (docs/analysis/state only),
so no unit/spotcheck/plan-diff run was owed.

In-flight: none.
