# M0106-0010 Step 3g — pg_index Form encoder + int2vector + nailed initial entries

Status: ACCEPTED
Date: 2026-05-17

## Problem

Step 3f wrote an empty heap-initialised page to `base/{1,5}/2610` so
PG's `RelationOpenSmgr → mdopen → BasicOpenFile` succeeds during the
critical-relation phase of standby start-up. Re-running
`TestE2E_FailoverGoopgToPG/async` after Step 3f advanced past the
file-existence FATAL but immediately hit the next blocker:

```
FATAL: cache lookup failed for index 2662
```

OID 2662 is `pg_class_oid_index`. During
`RelationCacheInitializePhase3`, after the empty `pg_index` heap file
is opened, PG iterates `criticalRelcachesBuilt[]` and calls
`SearchSysCache1(INDEXRELID, ObjectIdGetDatum(<oid>))` for every
nailed index. The lookup hits the brand-new init file's TupleDesc,
then deforms a heap tuple that **does not exist** — Step 3f's empty
page contains zero items — so the syscache miss propagates as
"cache lookup failed". Without per-index `Form_pg_index` rows the
relcache cannot finish building any index descriptor, and the
backend FATALs before serving a single query.

## Decision

Bootstrap one `Form_pg_index` heap tuple per nailed local + shared
index into `base/{1,5}/2610`, mirroring the established Step 3a–3e
pattern (`bootstrapPgProcTuples`, `bootstrapPgOpclassTuples`,
`bootstrapPgAmopTuples`). The row shape must match the upstream
PG18 21-column `FormData_pg_index` layout exactly so PG's
`GETSTRUCT()` cast lands every field at the right offset, and the
relcache init file's TupleDesc must declare the same 21 columns so
the in-memory tuple-deform agrees with the on-disk row.

### Column layout (upstream PG18 `pg_index.h`)

```
 1  indexrelid        oid             (fixed, 4)
 2  indrelid          oid             (fixed, 4)
 3  indnatts          int2            (fixed, 2)
 4  indnkeyatts       int2            (fixed, 2)
 5  indisunique       bool            (fixed, 1)
 6  indnullsnotdistinct bool          (fixed, 1)
 7  indisprimary      bool            (fixed, 1)
 8  indisexclusion    bool            (fixed, 1)
 9  indimmediate      bool            (fixed, 1)
10  indisclustered    bool            (fixed, 1)
11  indisvalid        bool            (fixed, 1)
12  indcheckxmin      bool            (fixed, 1)
13  indisready        bool            (fixed, 1)
14  indislive         bool            (fixed, 1)
15  indisreplident    bool            (fixed, 1)
─── variable-length region (PG `'i'` align) ─────────────────────
16  indkey            int2vector      (BKI_FORCE_NOT_NULL)
17  indcollation      oidvector       (BKI_FORCE_NOT_NULL)
18  indclass          oidvector       (BKI_FORCE_NOT_NULL)
19  indoption         int2vector      (BKI_FORCE_NOT_NULL)
20  indexprs          pg_node_tree    (NULLABLE — NULL for non-expr)
21  indpred           pg_node_tree    (NULLABLE — NULL for non-partial)
```

### Codec extension: `int2vector`

The varlena wire format for `int2vector` mirrors `oidvector`:

* 4-byte LE varlena header (`SET_VARSIZE`: `total<<2`),
* `ndim = 1`, `dataoffset = 0` (no null bitmap), `elemtype = 21`
  (INT2OID), `dim[0] = N`, `lbound[0] = 0`,
* `N × int2` payload (little-endian).

Sized at `24 + 2N` bytes. `internal/initdb/initdb.go::int2VectorBytes`
emits the blob. `internal/executor/codec.go::encodeValuePG` learns
the type-name `"int2vector"` and passes the pre-encoded `KindBytes`
through unchanged (callers in initdb already build the binary blob,
mirroring the `oidvector` path). `physicalPGTypeAlign` returns 4
(PG `'i'`) for the type so `att_align_pointer` lands correctly.

### Initial entries

The encoder pins all 21 nailed indexes from `nailedSharedRels` +
`nailedLocalRels`:

| OID | Index | Parent rel | Key cols (attnum) | Opclass | Unique | Primary |
|---|---|---|---|---|---|---|
| 2671 | pg_database_datname_index | 1262 | {2} | name_ops | yes | no |
| 2672 | pg_database_oid_index | 1262 | {1} | oid_ops | yes | yes |
| 2676 | pg_authid_rolname_index | 1260 | {2} | name_ops | yes | no |
| 2677 | pg_authid_oid_index | 1260 | {1} | oid_ops | yes | yes |
| 2695 | pg_auth_members_member_role_index | 1261 | {3,2,4} | oid_ops × 3 | yes | no |
| 3593 | pg_shseclabel_object_index | 3592 | {3,2,5} | oid/oid/text | yes | yes |
| 2703 | pg_type_oid_index | 1247 | {1} | oid_ops | yes | yes |
| 2704 | pg_type_typname_nsp_index | 1247 | {2,3} | name/oid | yes | no |
| 2658 | pg_attribute_relid_attnam_index | 1249 | {1,2} | oid/name | yes | no |
| 2659 | pg_attribute_relid_attnum_index | 1249 | {1,6} | oid/int2 | yes | yes |
| 2662 | pg_class_oid_index | 1259 | {1} | oid_ops | yes | yes |
| 2663 | pg_class_relname_nsp_index | 1259 | {2,3} | name/oid | yes | no |
| 2690 | pg_proc_oid_index | 1255 | {1} | oid_ops | yes | yes |
| 2691 | pg_proc_proname_args_nsp_index | 1255 | {2,20,3} | name/oidvector/oid | yes | no |
| 2679 | pg_index_indexrelid_index | 2610 | {1} | oid_ops | yes | yes |
| 2687 | pg_opclass_oid_index | 2616 | {1} | oid_ops | yes | yes |
| 2655 | pg_amproc_fam_proc_index | 2603 | {2,3,4,5} | oid/oid/oid/int2 | yes | no |
| 2693 | pg_rewrite_rel_rulename_index | 2618 | {2,7} | oid/name | yes | no |
| 2701 | pg_trigger_tgrelid_tgname_index | 2620 | {2,3} | oid/name | yes | no |
| 2667 | pg_constraint_oid_index | 2606 | {1} | oid_ops | yes | yes |
| 2688 | pg_operator_oid_index | 2617 | {1} | oid_ops | yes | yes |
| 2680 | pg_inherits_relid_seqno_index | 2611 | {1,3} | oid/int4 | yes | yes |
| 2654 | pg_amop_opr_fam_index | 2602 | {7,6,2} | oid/char/oid | yes | no |

For every entry:
* `indkey` = listed attnums in source-table order (PG semantics —
  `indrelid`'s attnum, *not* a positional column number).
* `indcollation = {0}` per key, except `name` / `text` keys use
  `C_COLLATION_OID = 950`.
* `indclass` = the per-key opclass OID (from Step 3b /
  `pg_opclass_d.h`).
* `indoption = {0, 0, …}` (no ASC/DESC override).
* `indisunique = true` for every nailed index (PG semantics —
  all critical indexes are unique).
* `indisprimary = true` when the index is the OID identity (`oid`
  is the relation's primary key).
* `indnatts = indnkeyatts = len(indkey)`.
* `indexprs = indpred = NULL` (no nailed index uses expressions or
  predicates).

### Two OID re-labellings vs `nailedLocalRels`

`internal/initdb/relcache_init.go::nailedLocalRels` carries
historical labels for two index OIDs that disagree with upstream
PG18 semantics:

* `2679` is labelled `pg_index_indrelid_index` but PG18's
  `pg_index_d.h` defines it as `IndexIndexrelidIndexId`
  (on `indexrelid`, attnum 1). The bootstrap row encodes the
  upstream meaning.
* `2655` is labelled `pg_amproc_oid_index` but PG18's
  `pg_amproc_d.h` defines it as `AccessMethodProcedureIndexId =
  pg_amproc_fam_proc_index` (on amprocfamily, amproclefttype,
  amprocrighttype, amprocnum). The bootstrap row encodes the
  upstream meaning.

The labels in `nailedLocalRels` are decorative — only the OIDs are
load-bearing — but the row contents must match the OID
semantics, otherwise PG looks up the wrong index when, e.g.,
`SearchSysCache1(INDEXRELID, …)` resolves to `2679`.

### Relcache init-file alignment

`internal/initdb/relcache_init.go::pgIndexAttrs()` expands 4 → 21
to match the on-disk row, and `nailedLocalRels` bumps pg_index
`relnatts` 4 → 21. Without this, PG's `heap_deformtuple` reads the
heap tuple under the wrong TupleDesc and lands `indkey` at attnum
4 (where `indislive` used to sit), causing
`RelationInitIndexAccessInfo` to dereference garbage.

## Tests

* `TestPgIndexColDefsMatchesRelcacheAttrs` (updated to 21-column
  assertion) — pins that the heap-tuple schema and the init-file
  TupleDesc declare the same 21 columns in the same order.
* `TestBootstrapPgIndexTuplesWritesHeapPagesToBase1And5` — pins
  that the page is heap-initialised (not zeroed) and that the
  resulting file is a non-zero multiple of `storage.BlockSize`
  (multi-page extents allowed once the row count exceeds one
  page's capacity).

## Verification

* `go test -count=1 -run 'TestPgIndex|TestBootstrapPgIndex'
  ./internal/initdb/` — PASS.
* `go test -count=1 ./internal/initdb/` — all PASS except the
  pre-existing `TestSynchronousCommitFlushesByDefault` (M0106-0012,
  baseline-stash confirmed unchanged).
* `go test -count=1 ./internal/executor/ ./internal/server/
  ./internal/storage/ ./internal/catalog/ ./internal/mvcc/` —
  all PASS.

## Files

* `internal/executor/codec.go` — `int2vector` case in
  `encodeValuePG` + `physicalPGTypeAlign`.
* `internal/initdb/initdb.go` — `int2VectorBytes`, `pgIndexEntry`,
  `pgIndexColDefs`, `pgIndexInitialEntries`, `pgIndexRow`,
  `bootstrapPgIndexTuples`.
* `internal/initdb/relcache_init.go` — `pgIndexAttrs()` 4 → 21,
  `nailedLocalRels` pg_index `relnatts` 4 → 21.
* `internal/initdb/pg_index_bootstrap_test.go` — regression
  pins updated for 21-column shape and multi-page heap layout.

## Next blocker (Step 3h)

Once Step 3g lands, re-run `TestE2E_FailoverGoopgToPG/async`. The
expected next FATAL is the `pg_amop_opr_fam_index` lookup path
hitting cross-type strategy rows that Step 3c–3e did not seed
(e.g. `int2 < int4`). Any further missing-row FATALs fall under
Step 3h or M0106-0011 (operational catcache invalidation).
