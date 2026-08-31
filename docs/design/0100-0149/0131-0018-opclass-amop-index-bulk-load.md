# 0131-0018 — Bulk-loading the opclass/amop lookup indexes so a hosted PG can sort

**Status:** accepted (implemented) — M0131-S12
**Measured by:** M0131-S4 finding F1 (`docs/design/0131-0004-forward-coldstart-e2e.md`
§Findings)
**Code:** `internal/initdb/btree_index_bootstrap.go`, `internal/initdb/initdb.go`
**Test:** `internal/testport/e2e_pg_coldstart_on_goopgdata_test.go`
(`assertHostedPGCanSort`)

## The gap

A real PostgreSQL 18.3 started on a `$PGDATA` that `goopg init` created could not
execute **any** sort, of **any** type:

```
postgres=# SELECT x FROM (VALUES (2), (1)) v(x) ORDER BY x;
ERROR:  could not identify an ordering operator for type integer
HINT:  Use an explicit ordering operator or modify the query.
```

Nothing in that query touches a goopg table — it is pure catalog resolution, so
every hosted-PG workload with an `ORDER BY`, a merge join, a `GROUP BY` that
sorts, or a B-tree build was blocked.

## Diagnosis

`ORDER BY` resolution runs `lookup_type_cache(typid, TYPECACHE_LT_OPR)`, which is
a **three-hop catalog walk**, and every hop is index-only:

| hop | function | catalog | index | pre-S12 state |
|---|---|---|---|---|
| 1 | `GetDefaultOpClass` (`postgres/src/backend/commands/indexcmds.c:2374-2384`) | `pg_opclass` (2616) | `pg_opclass_am_name_nsp_index` (**2686**) | empty placeholder |
| 2 | `get_opclass_family` | `pg_opclass` (2616) | `pg_opclass_oid_index` (2687) | already bulk-loaded |
| 3 | `get_opfamily_member` → AMOPSTRATEGY syscache | `pg_amop` (2602) | `pg_amop_fam_strat_index` (**2653**) | empty placeholder |

Then the planner's `get_ordering_op_properties` / `op_in_opfamily` reach the
AMOPOPID syscache list, i.e. `pg_amop_opr_fam_index` (**2654**) — also empty.

Hop 1 does `systable_beginscan(..., OpclassAmNameNspIndexId, indexOK = true, ...)`
with **no seq-scan fallback**; hops 3 and 4 are syscache lookups, which are
index-only by construction. In all three cases an empty index is
indistinguishable from "the row does not exist", so PG concluded that no type has
a default B-tree opclass.

The heaps were never the problem. Probed on the hosted PG: `pg_opclass` carries
177 rows, `int4_ops` is OID 1978 with `opcmethod` 403 and `opcdefault` = `t`, and
`pg_amop` carries the `int4` strategy rows. `pg_index` carried valid rows for
2686/2653/2654 all along. Only the index *content* was missing:
`internal/initdb/initdb.go` wrote all three as bare `makeBtreeRootPage()`
placeholders, while their siblings (2687, 2754, 2755, 2655) had real
bulk-load bootstrappers.

**The measurement that mattered:** fixing 2686 alone left the error message
*bit-identical*. Hop 1 and hop 3 fail the same way, so the first fix looked like
no fix at all. Anyone re-deriving this should isolate by hop
(`SET enable_seqscan = off` proves nothing here — the planner's index choice is
not the code path; the syscache is) rather than by symptom.

## What landed

Three bootstrappers, each built on an existing sibling's exact pattern, all
writing `base/1`, `base/5` and `global/`:

| index | keys | tuple encoder | sibling followed |
|---|---|---|---|
| 2686 `pg_opclass_am_name_nsp_index` | `(opcmethod, opcname, opcnamespace)` | `pgBuildIndexTupleOidNameOidKey` (80 B) | 2754 |
| 2653 `pg_amop_fam_strat_index` | `(amopfamily, amoplefttype, amoprighttype, amopstrategy)` | `pgBuildIndexTupleOidOidOidInt2Key` (24 B) | 2655 |
| 2654 `pg_amop_opr_fam_index` | `(amopopr, amoppurpose, amopfamily)` | `pgBuildIndexTupleOidCharOidKey` (24 B, **new**) | — |

`pgBuildIndexTupleOidNameOidKey` had named 2686 in its doc comment since the day
it was written without any caller ever building 2686's tuples — the encoder
existed, the wiring did not.

`bootstrapPgAmopTuples` previously discarded its heap TIDs; it now returns them,
which is what 2653/2654 are keyed on.

Two incidental corrections found by cross-checking against `pgIndexInitialEntries`:

- 2754's bulk-load passed `nkeyatts = 4` for a 3-key index. `nkeyatts` is baked
  into pivot tuples' `BTreeTupleGetNAtts`, and 2754 is multi-leaf, so the wrong
  value was on disk. Now 3, matching its `indnkeyatts`.
- 2686 is written to `global/` as well as `base/{1,5}`, matching what
  `bootstrapPgOpclassOidIndex` does for the same catalog's other index.

## Test direction inverted

`assertEmptyOpclassIndexStillBlocksSorts` was a deliberate fail-when-fixed
assertion (the `0131-0003` → S11 discipline). It fired exactly as designed and
is now `assertHostedPGCanSort`, asserting the positive: a hosted PG sorts
`(2), (1), (3)` to `1,2,3`. The `0130-0002` Guard #1 query at the call site got
its `ORDER BY c.relname` back and the Go-side `sort.Strings` was dropped, so the
guard now exercises ordering over a real goopg catalog scan rather than over a
`VALUES` list.

## Scope limit

**initdb-time only.** Every `$PGDATA` goopg has already created keeps the empty
indexes — the bench clusters (65433/65436/65437) and any operator directory need
a re-`initdb` to gain this. There is no runtime backfill path; see the deferral
ledger row for M0131-S12.

Also still narrow: goopg seeds only the default (`lefttype = righttype =
opcintype`) strategy operators for the pinned B-tree opclasses, so cross-type
comparisons resolved through `pg_amop` remain out of scope exactly as they were
before this change — the indexes are now complete with respect to the heap, and
the heap is what stays partial.
