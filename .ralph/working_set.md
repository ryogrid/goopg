(idle — nothing in flight)

Last loop: **M0127-P5.6-f-v** — DONE (harness + docs), committed, pushed.
Facts the next loop should NOT re-derive:

1. The DS05 gate now has a **third, non-blocking channel**: a named per-query
   status/runtime delta printed under the SUMMARY.
   `scripts/tpcds-sweep-diff.py OLD NEW` (+ `tpcds-sf05-regression.sh delta
   [OLD [NEW]]`, `SF05_NO_DELTA=1`, `SF05_SWEEP_BASELINE=<path|none>`).
   It parses the **sweep report itself**, so all ~90 archived reports are
   valid baselines — use `delta` to attribute any past regression for free.
2. Channel semantics worth remembering before changing it: both arms compare
   the INTERSECTION; TIMEOUT readings are the CAP and are excluded from the
   runtime arm; a runtime move needs ≥2× AND ≥5 s on the larger side; the
   default baseline SKIPS subset probes (a probe is "NOT a gate result").
3. Replay over all 87 adjacent archived pairs: 0 parse failures, 17 pairs with
   a verdict change; on the §5.15 pair (`sweep-20260804-214607` →
   `-232914`, identical SUMMARY lines) it prints `TIMEOUT +Q47 -Q72` and
   `SLOWER Q57 15s->81s (5.4x)`.
4. HEAD DS05 baseline refreshed: `sweep-20260805-090258` — PASS=94
   (57 ck-verified) MISMATCH=0 CKMISMATCH=0 ERROR=0 TIMEOUT=1 (Q47) SKIP=4,
   plan-shape `changed=0`, delta `verdict-changes=none`. A full sweep is
   ~35 min of query time (~40 min wall) — it fits a foreground Bash call.
5. A full-corpus EXPLAIN plan capture is ~14 s; `plans` + `delta` together
   answer "did this commit move anything?" without the 40-min sweep.

Gates run: UNITS green (cached — no Go source changed); full DS05 sweep green
with the channel live; `make ralph-state-guard` green (auto-repaired the
previous loop's completed marker); commit-hook pgbench smoke green.

Nightly triage 20260805-014309: unchanged — both items (AI-…-001
IsolationEvalPlanQual, AI-…-002 pgbench/nightly) already filed under M-NIGHTLY
and left unchecked per the banner. No new nightly run since.

Next step: per the banner (M0124 closed → M0125 → M0127), the open P5.6
successor is **M0127-P5.6-f-iv** — the functional-dependency arm PG has and
goopg lacks (`clauselist_selectivity`/`dependencies.c`), the real fix for
Q47 523 s / Q57 81 s. Its acceptance instrument now exists (item 1 above):
verify as a named-victim TIMEOUT-set diff, never a `TIMEOUT=` count.
**M0127-P5.6-d** stays BLOCKED on P5.7's batch-I/O term. Re-read the banner
before selecting.

In-flight: none.
