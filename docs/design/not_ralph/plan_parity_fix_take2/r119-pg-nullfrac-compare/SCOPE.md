# R119 SCOPE — PG18.3 comparison on R118's nullfrac inputs

R118 attributed goopg's lower-join 384-row divergence to ANALYZE-measured
outer-key nullfracs (`ss_hdemo_sk` 4.363% vs `ss_store_sk` 4.417%, inner
nullfracs 0) inside the `nd` equi-selectivity branch, with no model gap at
the pricing site. Per its boundary, a production change needs a PG18.3
comparison on THESE inputs. This round performs exactly that comparison.
It is measurement-only: no goopg source, test, flag, or config change.

## Questions (in order, stop at first negative)

1. **Stats inputs**: what are PG's `pg_stats.null_frac` for
   `ss_hdemo_sk`, `ss_store_sk`, `hd_demo_sk`, `s_store_sk` on the
   shared `:65438` `tpcds025` database? Record values (or absence).
2. **Model mechanics**: does PG's equi-selectivity for these joins use
   per-side nullfrac factors the way goopg's `pairNullSelectivity`
   does (`(1-nf1)(1-nf2)`, selfuncs.c `eqjoinsel` null-rejection)?
   Cite PG source lines; no experiment needed for this one.
3. **Estimated rows**: run both R101 forced forms on live `:65438`
   with the R111 session SETs (`work_mem=64MB`,
   `max_parallel_workers_per_gather=4`,
   `parallel_leader_participation=on`, both collapse limits 1) and
   read the lower Hash Join *estimated* rows per form. Compare the
   hdem-first/store-first estimate delta against goopg's 384 on
   common inputs — direction and magnitude, not equality (shared
   data is key-parity-sampled, NOT R111's clean inputs; R110's
   varchar repair does not apply here).

## Verdicts (exactly one)

- **(a) PG agrees** (same nullfrac-driven delta direction): close with
  no production change. The forced-form margin disagreement then lives
  in cost terms above selectivity (already R96/R98-series territory),
  and a new scope must say so with numbers.
- **(b) PG disagrees** (different/no nullfrac use, or materially
  different deltas): scope R120 with the exact divergence (function +
  lines both sides) and the minimal production-change shape. R120
  still requires its own review before code.

## Bounds

No Datum investigation (user-stopped R114 — do not resume under any
verdict). No goopg code/test/flag/config changes. No new clusters:
shared `:65438` read-only use (EXPLAIN + pg_stats reads only — no
ANALYZE, no DDL, no writes). Values are not re-gated (R111/R118
established 266; this round reads estimates, not results). If `:65438`
is held by a peer, wait or stop — never disturb a live bench.
