# R99 SCOPE — reproducible PG18.3 Q96 oracle provenance

R99 follows R98 (`801a54395`). R98 correctly declined to infer PG-private
inner-unique inputs. A fresh read-only check exposed a more basic prerequisite:
the currently available native-PG clone is not the historical Q96 oracle.

## Evidence

Native PostgreSQL 18.3 started successfully on the private `/tmp/r97goopg/ds025`
copy at port 65438. Against `postgres`, `pg_stats` returned zero rows for
`store_sales.ss_hdemo_sk`, `store_sales.ss_store_sk`,
`household_demographics.hd_demo_sk`, and `store.s_store_sk`. With Q96's
planning SETs, it produced `Aggregate → Gather → Nested Loop → Materialize`
with an index probe of hdem. This contradicts the historical statistics-backed
reference used by R95/R96 (`Finalize Aggregate → Gather → Partial Aggregate`
with two Hash Joins). The no-stats clone must not be used to adjudicate parity
or cost hypotheses.

## Authorized work

Provision a new, explicitly named private native-PG18.3 SF0.25 oracle copy
from either a stopped source cluster or a PostgreSQL-supported consistent
backup/restore. Never filesystem-copy a live data directory. Record the source
identity and immutable destination path before its first start, and prove that
no other postmaster/process uses either path before `ANALYZE`. Never alter
`postgres/`, the Goopg benchmark clone, a shared PG instance, or the historical
capture. On that private copy only:

1. record PG binary version, data-source provenance, database/schema, all Q96
   planner SETs, and server settings that affect planning;
2. record the exact ANALYZE command, statistics-target/server GUCs, and every
   Q96 participating relation/index's tuple, page, and size metadata. Run and
   record `ANALYZE` for the Q96 participating tables, then snapshot all
   `pg_stats`/`pg_statistic` entries for every predicate and join column Q96
   references (including `n_distinct`, null fraction, MCV values/frequencies,
   histogram bounds, correlation, and statistic targets). A pre-existing
   snapshot is acceptable only when it includes checksums of those actual
   statistic-catalog rows *and* the immutable data identity;
3. capture Q96 `EXPLAIN (VERBOSE, COSTS ON, FORMAT JSON)` twice under the
   exact settings, with byte identity, plus text EXPLAIN; capture value output
   and the four direct cardinality/filter witnesses; and
4. compare this new capture structurally to the historical PG reference. If
   it differs, record the exact divergence and close R99 as an oracle-drift
   report. Do not select whichever plan resembles Goopg.

The output must state whether this oracle is suitable for a later
inner-unique/selectivity investigation. PG-private semifactors remain
unobservable unless a subsequent scope derives them from these recorded
inputs and PG source without guessed constants.

## Boundaries and gates

R99 changes no Goopg production source, cost, defaults, planner behavior,
executor, query, PostgreSQL source, or historical artifact. `ANALYZE` is
allowed only on the disposable explicitly identified native-PG copy. It is not
a performance experiment and does not authorize a plan fix.

Before provisioning: agent review, correction, `git commit -n`, and push.
After capture: stop the private server; run `git diff --check`; commit and
push an English report with exact paths, commands, checksums/row counts, and
the oracle verdict. Any Goopg change requires a separate reviewed scope and
the normal full parity gates.
