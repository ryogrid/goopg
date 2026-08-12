# System-btree split registry coverage: the insert path outgrew its layout table

**Status:** accepted
**Date:** 2026-08-13
**Milestone:** M-NIGHTLY (AI-20260811-014635-002, AI-20260812-005501-004,
AI-20260813-005117-017 — `TestPort_IsolationReceiptReport`)

## Problem

`receipt-report.spec` failed in global setup, but not on anything the spec is
about:

```
ERROR:  DDL catalog sync: pg_index_indrelid_index for index:
        split sys btree 2678: split: unsupported system btree OID 2678
```

The three nightly items filed against this test had been carried as a suspected
SSI defect (the spec is a serializable read-only-deferrable spec). It is not an
SSI defect at all; the failure is in runtime system-catalog btree maintenance
and would reproduce under any workload that creates enough indexes.

## Root cause

goopg maintains the bootstrapped system btrees at runtime through two
independent halves:

- **insert** — `insertCanonicalSysBtreeLeaf` (`sys_catalog_index_insert.go`)
  appends an entry to the index's leaf-root page.
- **split / descent** — `splitSysBtreeLeafRoot`
  (`sys_catalog_btree_split.go`) and the multi-level descent
  (`sys_catalog_btree_multilevel.go`) restructure the index once that page
  fills. Both resolve the index's on-disk key layout through
  **`keyMetaForSysBtree`**, which refuses an unregistered OID.

The insert half never consults `keyMetaForSysBtree`. That asymmetry is the
whole bug: an index can be added to the insert path, work perfectly, pass its
own tests, and mirror correctly to the standby — for as long as its entries fit
on one leaf-root page. The first split then fails with
`split: unsupported system btree OID N`.

Nine indexes had accumulated in exactly that state, added across several
M0130/M0131 slices:

| index | OID | key builder | layout |
|---|---|---|---|
| `pg_index_indrelid_index` | 2678 | `buildIndexTupleOidKey` | {16, 1} |
| `pg_index_indexrelid_index` | 2679 | `buildIndexTupleOidKey` | {16, 1} |
| `pg_attrdef_oid_index` | 2657 | `buildIndexTupleOidKey` | {16, 1} |
| `pg_attrdef_adrelid_adnum_index` | 2656 | `buildIndexTupleOidInt2Key` | {16, 2} |
| `pg_rewrite_oid_index` | 2692 | `buildIndexTupleOidKey` | {16, 1} |
| `pg_rewrite_rel_rulename_index` | 2693 | `buildIndexTupleOidNameKey` | {80, 2} |
| `pg_sequence_seqrelid_index` | 5002 | `buildIndexTupleOidKey` | {16, 1} |
| `pg_extension_oid_index` | 3080 | `buildIndexTupleOidKey` | {16, 1} |
| `pg_extension_name_index` | 3081 | `buildIndexTupleNameKey` | {72, 1} |

Layouts are read off the builder each insert site actually calls, not inferred
from the catalog definition — the builders are the authority on the emitted
tuple size and MAXALIGN.

`receipt-report.spec` reached the fault on 2678 only at **permutation 152**,
which is why it read as a flaky/serialization problem: the spec's repeated
`CREATE INDEX`/DDL churn is simply what finally filled the leaf-root.

## Why the guard is a source pin

`TestEverySysBtreeInsertPathIndexHasSplitKeyMeta` parses the package with
`go/ast`, resolves the OID constant at every `insertCanonicalSysBtreeLeaf` call
site, and asserts `keyMetaForSysBtree` accepts it.

A value test cannot cover this class. The defect lives in the *relationship
between two call graphs* — nothing in the registry names the insert sites, and
nothing on the insert path reads the registry, so any assertion written against
either half alone is satisfied by the broken state. Only the source, where both
halves are visible at once, expresses "these two sets must agree". This is the
same reasoning as `TestHashBucketReadCallSitesPassTheFingerprint`
(`0118-0099`).

Two non-vacuity protections, because a source-scanning test degrades to a
silent pass when its scan breaks:

- a floor on the number of resolved call sites (currently 59; fails under 50),
  so a renamed helper or moved file fails loudly instead of checking nothing;
- an explicit failure when a call site's OID argument is not a resolvable
  literal identifier, rather than skipping it.

Verified fail-when-broken: removing the 2678/2679 registration makes the guard
name both OIDs and the exact runtime error they would produce.

## Alternative rejected

Making the *insert* path consult `keyMetaForSysBtree` and refuse an
unregistered OID would turn a deferred split failure into an immediate insert
failure — louder, but it converts a latent bug into an outage on catalogs that
are currently working, and it still would not tell a future author what to add.
The source pin fails at build/test time instead, which is where the omission is
actually made.

## Verification

- `TestPort_IsolationReceiptReport` — FAIL → PASS (6.8 s).
- `TestEverySysBtreeInsertPathIndexHasSplitKeyMeta` — PASS; proven to fail when
  a registration is removed.
- `go test ./internal/executor/` PASS; `go build ./...` + `go vet` clean.
- UNITS precommit PASS; `scripts/tpch-spotcheck.sh` PASS (Q12=2, Q13=35).

## References

- `internal/executor/sys_catalog_btree_split.go` — `keyMetaForSysBtree`
- `internal/executor/sys_catalog_btree_multilevel.go` — second consumer
- `internal/executor/sys_catalog_index_insert.go` — insert half + key builders
- `docs/design/0118-0099-predicate-hash-bucket-locking.md` — same source-pin pattern
- `docs/design/0130-0011-nbtree-pg-on-disk-format.md` — on-disk btree format
