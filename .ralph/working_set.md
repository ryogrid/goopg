Task: M0131-S30.9 — concurrent committed INSERTs are silently lost on a LIVE
goopg server (no crash). Filed + repro landed loop #141; ROOT CAUSE STILL OPEN.

**Read `docs/design/0131-0024` before doing anything else on S30.**

**The big result of loop #141: S30.8's premise is REFUTED.** S30.8 (and every
S30 conclusion drawn from `crashprobe30`'s atomicity line) assumed a replay
defect only because the probe evaluates the invariant exclusively after a
SIGKILL. The control was finally run — the same invariant fails with NO crash:
clean 16-client pgbench + clean shutdown gives history 60279 rows vs 60593
committed txns, and all four balance sums different. Do NOT change replay code
on S30.8's strength until S30.9's gate passes.

Files: `analysis/lostrows-concurrent-insert.sh` (NEW, the gate),
`docs/design/0131-0024-live-server-committed-insert-loss.md` (NEW),
`docs/design/README.md`, `.ralph/fix_plan.md` (S30.9 added, S30.8 marked
blocked), `.ralph/deferral_ledger.md`.

Key symbols (suspects, none confirmed): `Pool.flushBatch`
(`internal/storage/bufpool.go:2483` — AIO issue :2539 / Wait :2559 window, and
the dirty-bit clear at :2571 which re-checks the tag but NOT whether the page
changed after the bytes went to AIO); `batchExtendAndRegisterFSM` /
`selectFSMCandidatePage` (`internal/executor/operators_storage.go:8636-8703`).

Findings: `bash analysis/lostrows-concurrent-insert.sh` on a FRESH cluster →
`rows=75922 want=80000 heap_missing=6328 index_unreachable=5837`; all clients
exit 0 under `ON_ERROR_STOP=1`, zero error lines. 7.9% of committed rows gone
from the HEAP. The two counts are not nested (~491 ids absent from a seq scan
yet returned by an index scan) → heap and btree diverge both ways. Loss is
page-tail CONTIGUOUS (689-700, 1400-1419, 1983-2000, 2058-2067). Separate
effect: ~1.4% of pgbench_accounts rows go index-unreachable (heap row correct),
so later `UPDATE ... WHERE aid = ?` matches zero rows and silently drops the
increment (2000 updates left abalance=1); `REINDEX TABLE` repairs it.

RULED OUT, do not re-test: crash/replay (nothing killed; count comes from the
live buffer pool); client-side error; stale datadir (fresh `goopg init`);
single-client appends (exact at -c 1, and over 12 updates on a 200000-row
fillfactor=100 table); the ENTIRE S30.3 duplicate-buffer mechanism —
`GOOPG_PAGEIDENT_PROBE=1` fired ZERO events during a run that lost 4985 rows.

Next step: re-run the repro with `shared_buffers` well above the working set.
If the loss vanishes without eviction the defect is in the flush/victim path;
if it survives it is in the append path. That one run splits the search space.

Gates run: `RALPH_PRECOMMIT_SCOPE=units scripts/ralph-precommit-test.sh` PASS
(cached); `make ralph-state-guard` PASS (auto-repaired the previous loop's
completed marker); commit-hook pgbench smoke PASS. `RUNS=1 crashprobe30` FAIL
(atomicity only — but now known to be confounded by S30.9).

Nightly triage: `ci/logs/action-items.md` still run `20260811-014635`
(AI-…-001..012), all already filed under M-NIGHTLY; nothing new.

In-flight: none.
