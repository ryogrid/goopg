Task: M0138-0005 — corpus re-measure at a declared epoch.
**COMPLETE and committed/pushed** this loop (`2d5cf731d`), branch
`plan-parity-with-pg-take2-ralph`.

Files: `docs/design/0100-0149/m0138-0005-corpus-remeasure-declared-epoch.md`
(new), `docs/design/README.md` (+index row), `.ralph/deferral_ledger.md`
(reopened 1 row, filed 2 new rows), `.ralph/fix_plan.md` (M0138-0005 checked
off + DONE summary), `.ralph/progress.json` (state-guard repair). No
production code touched — this was a measurement-only recon task.

What was done: started the two down goopg lanes (`bench/tpch/setup_goopg.sh`,
`bench/tpcds/server.sh start sf025`; the two PG references were already up),
captured TPC-H (`estimate-audit -plan-only`) and TPC-DS SF0.25
(`scripts/capture-tpcds.sh`) plan sets, diffed vs PG with
`pg-plan-parity-diff.py`, and re-ran M0138-0001's per-column `pg_stats`
census (TPC-H 8 tables/61 cols, TPC-DS 5 tables/120 cols) post-M0138-0002/
-0003/-0004.

Key findings (full detail in the design doc):
  1. **TPC-H plan/category set is byte-identical** to the milestone-filing
     baseline (`METHODOLOGY3/README.md` "post-R128") — same match set
     (Q1/Q6/Q10/Q11/Q14/Q15a), all 9 category counts unchanged. Zero
     structural movement from the M0138 stats fixes (K50-consistent).
  2. **TPC-DS verdict tuple unchanged** (`2/69/0/25/3/0`, reproduces
     `TODO.md:4845-4849`'s R120 baseline exactly) but `join-order` and
     `qual-placement` each moved +1 with no verdict flip — one
     unidentified query's plan shape moved sideways. Ledger row filed,
     NOT bisected (99-query corpus, out of this recon task's budget).
  3. **`n_distinct` sign/order-of-magnitude agreement is now near-total**
     on both corpora (TPC-H 60/61, TPC-DS 120/120 sign match) — corpus-wide
     confirmation of the earlier spot-checks.
  4. **IMPORTANT CORRECTION**: the correlation-banding symptom M0138-0001
     found (`[0.09,0.16]` band, 36/118 TPC-DS columns vs PG's 7/119,
     concentrated on high-duplicate-density FK columns) was marked
     `resolved` by M0138-0004's `sort.SliceStable` tie-break fix — **that
     claim does not hold**. Post-fix re-measurement reads 36/120 vs 7/120,
     essentially unchanged. Source-level re-check confirms the tie-break
     port IS mechanically correct (both engines sort the reservoir into
     physical TID order first; PG's `tupno` is exactly the post-sort array
     index, same as goopg's `pos`) — so the fix stays landed as a genuine
     improvement, it just isn't the (sole) cause of the corpus symptom.
     **Reopened** the `m0138-0001` ledger row (status flipped `resolved`
     -> `-`) with a corrected resume point: build a synthetic table with a
     KNOWN physical-position/value relationship, load identically on both
     engines, compare correlation directly — this is the only way to
     separate "goopg computes it wrong" from "the two storage engines
     genuinely place these TPC-DS SF0.25 rows differently for a reason
     outside ANALYZE's control" (M0138-0001's original alternative
     hypothesis, never eliminated, now the leading one).
  5. **New, narrower finding**: `avg_width=0` still reproduces for goopg's
     fast-path `numeric` (int64 mantissa inline, no heap arena) —
     M0138-0004's typlen fallback correctly only covers fixed-width types,
     but `numeric`'s measured-payload branch only measures the slow
     (big-numeric) path. Reproduces on 28/61 TPC-H columns (every
     numeric-typed key/price/cost column — HammerDB declares TPC-H keys
     `numeric`) and 17/120 TPC-DS columns. Ledger row filed; needs PG's
     actual numeric-varlena size formula (value-dependent), not a literal
     constant (anti-tuning rule forbids a guessed constant here).

Key symbols: `computeColumnStats`/`datumVariablePayloadWidth`/the
`corrPairs` sort (`internal/executor/operators_analyze.go:1159-1328`),
`analyzeSampleByTID` (`:952-953`, the TID-sort that both engines share),
PG oracle `compare_rows` (`analyze.c:1358-1379`, confirms PG sorts `rows[]`
by physical TID too) and `compute_scalar_stats`'s `tupno` assignment
(`analyze.c:2495`, confirms `tupno` IS the post-sort array index).

Gates run: `go build ./...` clean (no source changed). `make
ralph-state-guard`: found and auto-repaired one stale
status/progress.json inconsistency, then consistent. Pre-commit hook's
pgbench smoke: PASS (144 TPS, 0 failed). No values-gate re-run needed —
no production code touched this loop.

Bench lanes: `:65433` (goopg TPC-H) and `:65437` (goopg TPC-DS SF0.25)
were started this loop (idempotent, no `--reset`/data touch) and left
running per the shared-`:6543x`-lane rule (verify, never restart). `:65432`
and `:65438` (PG references) were already up at loop start.

In-flight: none.

Next step: Per the banner, re-check `.ralph/fix_plan.md`'s `## Current
Priority` banner fresh. M0138's own task list now has only M0138-0006 left
("re-measure Q9's estimate against R130's table and reconcile the ANALYZE
seed") — a good next pick, OR the banner's independent-trio rule permits
picking the topmost unblocked M0139 (`M0139-S1`, now unblocked since
M0137-0010 landed) or M0140 (`M0140-0001`) task instead, whichever the next
loop judges most valuable. Two open threads from THIS loop worth a future
loop's attention but not yet actionable as a fix_plan task on their own:
the correlation-banding synthetic-test resume point (item 4 above) and the
numeric fast-path avg_width formula (item 5) — both already have ledger
rows with concrete resume points, pick them up when a task explicitly
targets ANALYZE precision again (most likely M0138-0006 or a future
M0138-000N).
