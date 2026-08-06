(idle — nothing in flight)

Last loop: **M-NIGHTLY AI-20260806-011323-002..-015 — one mid-run compile
error filed itself as 14 phantom testport regressions. FIXED, committed.**
Selected under the carve-out: this DOES break a gate M0127 depends on —
P6.1–P6.4's S7 bar is "S5-ON survives a clean nightly cycle", and a nightly
that manufactures regressions whenever it overlaps an active Ralph loop can
never come back clean. The gate was unmeasurable, not merely noisy.

1. **Option (a) from the filing was already implemented and did NOT help.**
   `stage-preflight.sh` has always run `make build` and aborted; in run
   `20260806-011323` preflight PASSED in 7 s and testport broke 1079 s later
   (`s.traceFailed undefined`, from the previous loop's own `bf52391e`). The
   tree mutates *between* stages, so no front-loaded build can catch it.
2. **Boundary, not flag.** `build_error_line()` returns the *index* of the
   first Go build signature; tests failing at/after it collapse into ONE
   `[infra]` item, tests failing before it are reported normally. Load-bearing:
   `TestPort_IsolationEvalPlanQual` (genuine, six nights) failed ~600 lines
   above the boundary in that same log. The boundary also catches the 8
   `pg_dump*` victims carrying **no compiler text at all** (`start failed;
   process exited early`) — no message-matching rule could have.
3. **Non-gating on purpose.** `build_kills` → `inconclusive`, like a resource
   kill: the recorded sha builds clean. Gating it would keep the S7 bar
   permanently unmeetable — the defect restated.
4. `source_fingerprint()` (HEAD + `*.go`/`go.mod`/`go.sum` porcelain + tracked
   diff) stamps `meta.json` + `stages/<name>.fp` before each stage, so drift is
   stated rather than inferred.

Files: `ci/batch/lib/summarize.py`, `ci/batch/lib/common.sh`,
`ci/batch/run-nightly.sh`, `ci/batch/lib/test_summarize.py` (new
`MidRunBuildBreakTest`), `ci/design/04-logging-and-reporting.md` §C.1 + README
index, fix_plan (item ticked + S7 gate amendment), 1 ledger row.

Gates run: `test_summarize.py` 8/8 PASS, non-vacuous (forcing
`tp_build_boundary = None` fails 2); real-run replay `20260806-011323`
**18 items → 5** (4 genuine + 1 infra, EvalPlanQual still -001); `bash -n` on
both shell files; fingerprint stability + edit-detection probe; pgbench smoke
via hook. No Go changed, so UNITS/SPOT/DS05 not applicable.

NEXT LOOP (banner wins — M0124 → M0125 → M0127; M-NIGHTLY still parked).
M0127's topmost unchecked item is **P6.1 — delete fusion**, still gated on a
clean nightly cycle. Read the newest `ci/logs/action-items.md` FIRST: P6.1 is
selectable only at `status: pass`. Note the next nightly is the first to run
the new summarizer, so a `[infra] testport/build-broke-mid-stage` item now
means "the harness saw the loop edit the tree", not a regression.

In-flight: none.
