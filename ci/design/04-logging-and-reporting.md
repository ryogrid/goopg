# 04 — Logging & Reporting

## A. Run directory layout

Every invocation (scheduled or manual) creates one timestamped directory:

```
ci/logs/
  scheduler.log                  # the resident daemon's own log (append-only; daemon lines only)
  launch.log                     # scheduled firings' bootstrap stdout/stderr (only pre-run-dir failures land here)
  action-items.md                # agent-facing failure feed, regenerated every run (doc 07; stable path)
  latest -> 20260707-000012/     # symlink, updated at run start
  20260707-000012/
    progress.log                 # REAL-TIME batch progress (see §B)
    meta.json                    # git SHA, dirty-file list, go version, host, start/end, config snapshot (caps, ports, budgets)
    summary.md                   # human summary (see §D)
    summary.json                 # machine summary (see §D)
    preflight/
      build.log
      checks.log
    units/go-test.log
    race/go-test.log
    testport/
      go-test.log                # full -v output (regress+isolation subtests included)
      results.csv                # parsed per-test: name,status(pass|fail|skip),elapsed
    pgbench/
      pgbench.log                # -i -s 50 load + standard/-N/-S runs (c=100 j=20 T=180)
      server.log
    tpch/
      spotcheck.log
      explain-run.log            # raw tpch-runner -explain output (source of explain/)
      explain/q01.txt … q22.txt  # EXPLAIN plans, split per query (requirement 5)
      run.log                    # tpch-runner full output
      timings.csv                # query,elapsed_s,rows,status(ok|timeout|error|not-run)
      plan-gate.log              # informational structural diff (if server was up)
```

`ci/logs/` is **gitignored** (implementation adds the entry); `ci/design/`
and `ci/batch/` are committed.

## B. Real-time progress log (requirement 9)

`progress.log` is written by the orchestrator only — one line per event,
flushed immediately (`tee -a` from a line-buffered emitter in
`ci/batch/lib/common.sh`), so `tail -f ci/logs/latest/progress.log` is the
live view:

```
2026-07-07T00:00:12+09:00 [RUN]    start sha=04f2f6cf dirty=7 config=default
2026-07-07T00:00:14+09:00 [S0]     preflight start
2026-07-07T00:03:40+09:00 [S0]     preflight ok (build 186s)
2026-07-07T00:03:41+09:00 [S1.L]   units start
2026-07-07T00:03:41+09:00 [S1.H]   testport start (unit=goopg-nightly-testport high=6G max=8G)
2026-07-07T00:13:52+09:00 [S1.L]   units PASS (611s)
2026-07-07T00:13:53+09:00 [S1.L]   race start
...
2026-07-07T01:42:10+09:00 [S2]     tpch q21 ok 301.2s rows=397 (budget left 4310s)
...
2026-07-07T02:55:03+09:00 [RUN]    end status=PASS regressions=0 promotable=1 (see summary.md)
```

Per-test detail stays in the per-stage logs; `progress.log` carries stage- and
TPC-H-query-granularity only, so it stays readable across the whole night.

## C. Classification & the perf-tolerance policy (requirement 6)

Because the Ralph loop runs alongside, wall-clock numbers are noisy by
design. The policy:

**Gating (can fail the run):**
- any must-pass test failure (doc 02 §B);
- a baseline-`pass` regress case reporting an output-mismatch skip (the
  doc 02 §A join rule — regress divergence surfaces as SKIP, not FAIL);
- pgbench nightly run (s=50 c=100 j=20 T=180×3): any failed transaction
  (functional gate — TPS is NOT gating);
- TPC-H: spotcheck row-count mismatch (Q12/Q13 vs
  `bench/tpch/spotcheck_expected.env`), a query returning wrong row counts vs
  the anchors table, or a query erroring;
- build failure (top-level `fail`, stage detail `fail(build)`).

**Flagged as `perf-drastic` (fails the run) ONLY on:**
- a TPC-H per-query timeout/cancel (57014) under the doc-05 budget;
- TPC-H total-budget exhaustion (queries left `not-run(budget)`);
- TPC-H total elapsed > **2×** the 20260526 baseline total (2 × 1469 s ≈ 49
  min) *when all 22 queries completed* — a whole-sweep doubling is beyond
  plausible co-load noise.

**Recorded but NEVER flagged:**
- per-query elapsed drift below the above (written to `timings.csv`, compared
  in `summary.md` against the 20260526 per-query column for the human eye);
- pgbench nightly TPS;
- stage durations (units/race/testport) — tracked in `summary.json` as the
  batch's own trend series.

**Informational extras:**
- `make plan-gate` structural plan diff (SKIP-tolerant); a shape change is
  reported as `plan-drift` for human review, not failed — plan churn can be
  legitimate planner work by the loop;
- `promotable` notices (expected-fail passed);
- `env-drift` (newly skipped tests).

## D. Summary artifacts

`summary.json` (schema, one object per run):

```json
{
  "run_id": "20260707-000012",
  "sha": "04f2f6cf", "dirty_files": 7,
  "status": "pass|fail|inconclusive|aborted",
  "stages": {
    "preflight": {"status": "ok", "elapsed_s": 208},
    "units":     {"status": "pass", "elapsed_s": 611},
    "race":      {"status": "pass", "elapsed_s": 902},
    "testport":  {"status": "pass", "elapsed_s": 4210,
                  "counts": {"pass": 350, "fail": 0, "skip": 12},
                  "expected_fail_hit": ["deadlock-parallel.spec"],
                  "promotable": []},
    "pgbench":   {"status": "pass", "elapsed_s": 170, "failed_txns": 0},
    "tpch":      {"status": "pass", "elapsed_s": 1523,
                  "completed": 22, "timeouts": 0, "not_run": 0,
                  "spotcheck": {"q12": 2, "q13": 33},
                  "total_vs_baseline": 1.04}
  },
  "regressions": [], "promotable": [], "resource_kills": [],
  "notes": ["plan-drift: Q2 join order changed (informational)"]
}
```

`summary.md` renders the same content for humans: verdict line, regression
table (empty = green), promotable list, TPC-H per-query table (elapsed vs
20260526 baseline, rows vs anchors), skip/env-drift list, resource-kill
forensics if any.

`ci/logs/action-items.md` is derived from the same `summary.json` in the same
S3 pass: every gating failure becomes an `AI-<run>-<seq>` item with a
copy-pasteable repro command and evidence paths; non-gating notices go to its
bottom section. Full format and the fix_plan consumption contract: doc 07.

**Exit code contract (normative):** `run-nightly.sh` exits
- **0** — `status == "pass"`;
- **2** — `fail` (any regression, perf-drastic, or build failure);
- **3** — `inconclusive` (resource-kill);
- **4** — `aborted` (signal/operator);
- **5** — run lock already held (doc 06 §C; no run dir is created).

Top-level `status` stays the four-value enum; finer detail (e.g.
`fail(build)`, `skip(no-data)`) lives in the per-stage `status` strings.

## E. Retention & trend

- Keep the newest `NIGHTLY_KEEP` (default **14**) run dirs; the summary stage
  prunes older ones (never touches `scheduler.log`).
- Additionally append one line per run to `ci/logs/history.jsonl` (the
  `summary.json` condensed to run_id/sha/status/stage-durations) — this file
  is small, survives pruning, and is the long-term trend record the perf
  policy's "batch's own trend data" relies on.
