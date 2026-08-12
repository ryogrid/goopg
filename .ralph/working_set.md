(idle — nothing in flight)

M0131-S30 CLOSED (loop #17). Verification-only loop: NO code changed.

Files: `.ralph/fix_plan.md` (S30 → [x] with the closure evidence),
`docs/design/0131-0020-crash-recovery-row-loss-confirmed.md` (closure section
appended), `docs/design/README.md` (index line updated), one ledger row.

Worth carrying:
- S30's gate (`RUNS=3 bash analysis/crashprobe30.sh`) went from 3/3 FAIL to
  6/6 PASS with NO new code. The cause was the previous loop's S32 fix
  (`const maxChain = 64` in every HOT/CTID chain walker). S32 had been FILED
  with the prediction that it "plausibly contributes to S30's crash-probe
  divergences" — so re-running an open item's own gate after a neighbouring
  fix landed is worth doing BEFORE opening a fresh investigation. This cost
  ~12 minutes and closed a 4-loop estimate.
- Reading the probe's output correctly: the per-run `sum(abalance)` values
  differ wildly and change sign (+71742 … −747056). That is EXPECTED — each
  run kills pgbench at a different point. The assertion is only that the two
  sums AGREE. Do not read a large negative sum as a failure.
- Crash recovery still has NO automated gate; `crashprobe30.sh` is manual and
  ~6 min for 3 runs. That residual is the ledger row and belongs to the open
  S27/S28.

Gates: `RUNS=3 analysis/crashprobe30.sh` PASS twice (6/6 runs; logs
`/tmp/cp30_head_fb8affdb.log`, `/tmp/cp30_head_confirm.log`), `go build` clean,
pgbench smoke via the commit hook, `make ralph-state-guard` OK (auto-repaired
the stale completed marker, same as last loop).

Nightly triage: `ci/logs/action-items.md` still run `20260812-005501` (unchanged
for 3 loops); all 4 `## AI-` items already filed under M-NIGHTLY, nothing new.

Next loop (banner = M-NIGHTLY filing, then M0131): next unchecked M0131 slice.
Remaining in file order: S9 (LARGE), S8b, S21 (LARGE), S24, S26, S27, S28.
S27/S28 now carry S30's automation residual, which argues for taking one of
them next.

In-flight: none.
