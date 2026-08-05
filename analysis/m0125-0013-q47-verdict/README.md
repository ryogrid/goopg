# M0125-0013 (bookkeeping half) — Q47's runtime verdict, measured

**Date** 2026-08-03 · **HEAD** `374dc60e` · **Host** verified quiet
(`pgrep -af run-nightly.sh` empty; load 0.28–1.21, versus load ~10 under which
every 2026-07-30 Q47 timing was taken).

Both engines measured back to back on the same host, same query file
(`bench/tpcds/runtime_goopg/tpcds-data/queries/query47.sql`), SF=1.

| reading | goopg (:65436 `postgres`) | PG 18.3 (:65438 `tpcds`) |
|---|---|---|
| Q47 wall | **537.55 s** | **3.38 s** |
| rows | 100 | 100 |
| values | **byte-identical** (`diff` clean) | — |

## Verdict

Both primary sources are wrong, in opposite directions.

- `analysis/tpcds-sf1-resweep-20260728/RESULTS.md` chunk 49–56 — *"the 8.4× is
  the **expected cost** of real work; Q47 is NOT a regression"* — **REFUTED**.
  Nothing PG answers in 3.4 s makes 537 s an expected cost. The same refutation
  lands on the `tpcds-round2 RC-1b` ledger row's *"(14s->143s confirms real
  work)"*, which that chunk cites as corroboration.
- `analysis/tpcds-sf1-goopg-20260728.md` §3.2 had the right **direction** for a
  since-falsified **reason** ("the row count did not move" — it has moved to 100
  and the query got *slower*); §6 item 2's "**bounded** but **unattributed**" is
  now wrong on both words.

## Attribution (measured, not argued)

1. **The CTE is not the cost and is not recomputed.**
   `internal/executor/operators_cte_dml.go:214` (`cteScanOp.Open`) keys
   `ctx.CTERowCache` on the **CTE name**, so `v1`, `v1_lag` and `v1_lead` share
   one evaluation even though `EXPLAIN` renders the body under all three
   `CTE Scan` nodes. Standalone on a fresh server: **52.28 s / 63,745 rows**.
   ⇒ ~485 s of the 537 s is the `v2` three-way self-join alone.
2. **That join degenerates to a single low-cardinality hash key.**
   `internal/planner/planner.go:5249` (`splitEqualityForHash`) returns on the
   **first** disjoint-side equality conjunct — for Q47 that is
   `v1.i_category = v1_lag.i_category`.

   | key over v1 (63,745 rows) | distinct |
   |---|---|
   | `i_category` (what goopg hashes on) | **10** |
   | `i_brand` | 704 |
   | `s_store_name` | 6 |
   | `s_company_name` | **1** |
   | 4-key composite (what PG merge-joins on) | **5,667** |

   ⇒ ~6,374-row buckets where a composite key gives ~11: a **~567× over-scan per
   probe**, twice (two nested hash joins). PG instead uses two **Merge Joins over
   all four keys** with `rn` as the join filter.
3. **Pre-existing, already filed — not an RC-1b regression.** The single
   `LeftKey`/`RightKey` limitation is ledger rows `M0125-0011` (2026-07-29) and
   `M0125-0035` (2026-08-01). RC-1b changed **reachability**: at set A the CTE
   body was empty so the self-join did nothing (17 s); each later fix let more of
   the pipeline execute — 17 s (empty) → 142 s (partial) → 537 s (fully correct).
   The runtime climbed *because* the answer got right.

## Files

| file | what |
|---|---|
| `goopg-q47.txt` / `goopg-q47-rows.txt` | goopg timed run + full 100-row output |
| `pg-q47.txt` / `pg-q47-rows.txt` | PG timed run + full 100-row output |
| `q47-value-diff.txt` | empty — the two outputs are byte-identical |
| `goopg-q47-explain.txt` / `pg-q47-explain.txt` | plan shapes |
| `goopg-v1-only.txt` | the `v1` CTE body alone, one evaluation, fresh server |
| `q47-key-cardinality.txt` | join-key cardinalities over v1 |
