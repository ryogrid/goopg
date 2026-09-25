# R108 — PG hash-tuple sizing comparison: report

## Outcome

The default-off `GOOPG_PG_HASH_TUPLE_SPILL_COST=1` experiment is correct as a
cost-model boundary, but did not change a selected plan in the measured
corpora. It must not be promoted to the default from this evidence.

The switch replaces only the Hash Join spill-I/O price's batch and page
geometry. It deliberately retains Goopg's map-capacity and executor-spill
geometry. `DPPGHASH` proves which currency was used and records the separate
packed hash-tuple and aligned heap-page calculations.

For the Q96 common-data witness, the selected hash candidates used the PG
geometry with `pgbatches=1`; no spill I/O is due in either currency. This
explains the unchanged forced and natural Q96 plans rather than treating the
result as evidence that Datum representation never matters.

## Verification

The implementation passed:

```text
go test ./internal/optimizer
go test ./internal/executor ./cmd/estimate-audit
go vet ./internal/optimizer ./internal/executor ./cmd/estimate-audit
git diff --check
```

On fresh R111 common-data clusters, both forced Q96 forms still returned
`266`; OFF/ON JSON result hashes were identical for both forms, and the
natural Q96 value and EXPLAIN hash were also identical.

TPC-DS SF0.25 results were unchanged in both arms:

```text
OFF: PASS=96 (60 ck-verified, 36 ck=n/a), MISMATCH=0, CKMISMATCH=0,
     ERROR=0, TIMEOUT=0, SKIP=3
ON:  PASS=96 (60 ck-verified, 36 ck=n/a), MISMATCH=0, CKMISMATCH=0,
     ERROR=0, TIMEOUT=0, SKIP=3
```

The two 99-query EXPLAIN captures have no native plan movement:

```text
PLAN-SHAPE: queries=99 same=99 changed=0 added=0 removed=0
```

A fresh live PG18.3 capture on `:65438` against `tpcds025`, with
`work_mem=64MB` and `max_parallel_workers_per_gather=4` pinned in session,
gave the same structural census for both Goopg arms:

```text
queries=99 match=2 shapediff=69 unparsed=0 missingnode=25 error=3 timeout=0
```

The two parity reports differ only in their input-path header.

TPC-H SF1 has matching values for the 23 completed digest entries in OFF and
ON. Q9 reached the fixed 600-second per-query cap in both arms. Its client
error wording differed (`user request` versus `statement timeout`), so the
digest comparator reports one `ERROR-DIFF`; the existing baseline likewise
reports Q9 as a status difference. This is a coverage limitation of the run,
not a value or plan movement attributed to R108.

## Decision

Keep the experiment default-off and retain the separation between planner
parity geometry and executor capacity. R108 only tests the PG hash spill
term; it cannot answer whether using PostgreSQL Datum sizes throughout all
cost computations would change elections. That broader question is a new,
separately scoped task and depends on this negative R108 result.
