Task: M0131-S32 — FIXED and committed. Successor filed: **M0131-S32.1**.

Root cause (loop #144): every HOT/CTID chain walker capped its loop at a
hand-written `const maxChain = 64`, so version 65 of a repeatedly-HOT-updated
row was unreachable. The index scan driving `UPDATE … WHERE id=1` then found no
live tuple, wrote NOTHING, and still reported `UPDATE 1`. The "S31
index-unreachable" symptom in the same repro was a CONSEQUENCE of the same
truncation, not a second defect, and `pruneChainTip`'s identical cap is why the
page looked permanently full.

Fix: new PG-faithful `storage.MaxHeapTuplesPerPage` (291 at 8 KiB — the exact
bound, since a HOT chain visits distinct slots of ONE page) for the four
single-page walkers; explicit `maxCTIDChainWalk = 1<<20` corruption backstop for
the two CROSS-page t_ctid walkers (PG's `heap_get_latest_tid` is uncapped).

Files: `internal/storage/heap.go` (const + parity test), `internal/storage/prune.go`,
`internal/executor/operators_index.go`, `internal/executor/operators_storage.go`,
`internal/executor/hot_update_test.go` (new `TestHOTUpdateChainBeyond64Versions`),
`internal/storage/heap_test.go`, `analysis/atomicity-nocrash-control.sh`
(now asserts tellers/branches), `docs/design/0131-0025-single-row-update-stall.md`,
`docs/design/README.md`, `.ralph/fix_plan.md`, `.ralph/deferral_ledger.md`.

Next step: work **M0131-S32.1** — the concurrent arm, still wrong in BOTH
directions with the S32 fix in place (16 clients, scale 5, no crash:
`sum(delta) = -31482`, `sum(abalance)` exact, `sum(tbalance) = -14938`
under-applied, `sum(bbalance) = -309543` ~10x OVER-applied, identical LIVE and
after a clean restart). Gate: `RUNS=1 LOADSEC=30 bash
analysis/atomicity-nocrash-control.sh` (its header records `OVERALL: FAIL` as
the KNOWN state — read the per-table lines). Start at the duplicate-live-index-
entry path for the over-application: `internal/executor/operators_storage.go:4244`,
whose AI-20260810-011258-006 guard dedupes only WITHIN one statement's `pending`
set. Tellers' under-application has the opposite sign — measure the two tables
separately before assuming one cause. Second, independent follow-up in the
ledger: an UPDATE's reported row count still comes from the planned row set, not
from rows actually written.

Ruled out for S32, do not re-test: `tryApplyHOTUpdate`'s under-lock
`isConcurrentlyUpdated` re-check, `isConcurrentlyUpdated` itself, the `!used`
EPQ fallback, and the page-capacity hypothesis — the cause was the walk bound.

Gates run: `analysis/hotstall.sh` PASS at N=300 AND N=2000 (was FAIL at 64);
`TestHOTUpdateChainBeyond64Versions` PASS, and confirmed it FAILs at exactly
"after update 65: v=64" with the bound put back to 64;
`TestMaxHeapTuplesPerPagePGParity` PASS; `RALPH_PRECOMMIT_SCOPE=units
scripts/ralph-precommit-test.sh` PASS (executor+storage rerun, rest cached);
`scripts/tpch-spotcheck.sh` RESULT=PASS (Q12=2, Q13=35); commit-hook pgbench
smoke PASS; `make ralph-state-guard` PASS (auto-repaired the stale completed
marker). NOT run: TPC-DS SF0.5 gate (~1 h) — the change is a chain-walk bound
with a deterministic repro plus the spotcheck; run it next loop if S32.1 work
touches the same paths.

Nightly triage: `ci/logs/action-items.md` still run `20260811-014635`
(AI-…-001..012) — all 12 already filed under M-NIGHTLY; nothing new to file.

In-flight: none.
