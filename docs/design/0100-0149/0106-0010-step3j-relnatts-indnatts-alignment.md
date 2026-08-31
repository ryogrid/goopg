# M0106-0010 Step 3j — pg_class.relnatts must agree with pg_index.indnatts

Status: implemented (2026-05-17)

## Problem

After Step 3i (PG-conformant null-bitmap encoding) the PG standby boot
advanced past the "cache lookup failed for index 2662" FATAL but
immediately hit:

    FATAL: relnatts disagrees with indnatts for index 2662

The check is in `postgres/src/backend/utils/cache/relcache.c:1490-1493`
inside `RelationInitIndexAccessInfo`:

    indnatts = RelationGetNumberOfAttributes(relation);
    if (indnatts != IndexRelationGetNumberOfAttributes(relation))
        elog(ERROR, "relnatts disagrees with indnatts for index %u",
             RelationGetRelid(relation));

`RelationGetNumberOfAttributes(relation)` reads `rd_rel->relnatts` —
i.e. `pg_class.relnatts` — and
`IndexRelationGetNumberOfAttributes(relation)` reads
`rd_index->indnatts` — i.e. `pg_index.indnatts`. The two must agree.

## Root cause

`internal/initdb/relcache_init.go::flattenRels` historically built every
nailed index with a hardcoded `RelNatts = 2`:

    func flattenRels(heaps []nailedRel, idxs []idxSpec) []nailedRel {
        var out []nailedRel
        out = append(out, heaps...)
        for _, idx := range idxs {
            out = append(out, indexNailed(idx.OID, idx.Name, 2))  // !!
        }
        return out
    }

That single fixed value was wrong for every single-column nailed index
in the local + shared lists (16 of 23). Step 3g landed full
`Form_pg_index` rows whose `indnatts` faithfully reflects the actual
key count (e.g. `pg_class_oid_index` is `[oid]` so `indnatts = 1`), and
the disagreement immediately became fatal.

`pg_class_oid_index` (OID 2662) was the first index PG opens during
critical-relation init and so the canary site for the bug — but every
1-key index would have FATALed the same way.

## Fix

Single source of truth: derive each index's natts from
`pgIndexInitialEntries`, which already encodes the correct
`indnatts = len(IndKey)`.

### `internal/initdb/initdb.go`

New helper next to `pgIndexInitialEntries`:

    func pgIndexNattsByOID() map[uint32]int16 {
        entries := pgIndexInitialEntries()
        out := make(map[uint32]int16, len(entries))
        for _, e := range entries {
            out[e.IndexRelid] = int16(len(e.IndKey))
        }
        return out
    }

### `internal/initdb/relcache_init.go`

`flattenRels` consults the map per-index instead of the literal `2`:

    natts := pgIndexNattsByOID()
    for _, idx := range idxs {
        n, ok := natts[idx.OID]
        if !ok {
            n = 1
        }
        out = append(out, indexNailed(idx.OID, idx.Name, n))
    }

The fallback `n = 1` keeps the table-walk robust if a future index is
added to `nailedSharedRels` / `nailedLocalRels` before its
`pgIndexInitialEntries` row lands — but the test pin below catches
that gap loudly.

The flow-through is automatic:
- `indexNailed(oid, name, n)` populates both `RelNatts` and
  `Attrs = indexKeyAttrs(n)` (one `oid` placeholder per key column).
- `pgClassRow` reads `RelNatts` into the heap tuple's `relnatts`
  (offset 120).
- `buildPgClassBlob` reads the same field into the init-file
  pg_class blob (offset 120).
- `bootstrapPgAttributeTuples` walks `rel.Attrs` and emits exactly
  `len(Attrs)` pg_attribute heap rows per index.

So the change keeps the heap data and the init-file data byte-for-byte
consistent with each other and with the corresponding pg_index row.

## Scope deliberately not addressed

This step fixes only the count agreement. Per-column type fidelity is
still placeholder — `indexKeyAttrs(n)` produces `n` rows all typed as
`oid`, even when the real index key is e.g. `(name, oid)`. PG's later
index-scan paths read column types from pg_attribute and may yet surface
follow-up assertions; those are filed separately and will fall out of
the next E2E re-run.

## Regression pins

`internal/initdb/pg_index_relnatts_test.go`:

- `TestNailedIndexRelnattsAgreesWithIndnatts` walks every entry in
  `nailedSharedRels` and `nailedLocalRels` with `RelKind == 'i'`,
  asserts that the matching `pgIndexInitialEntries` row exists, and
  that both `RelNatts` and `len(Attrs)` equal that row's `indnatts`.
- `TestPgClassOidIndexHasSingleKeyColumn` pins the specific Step 3j
  canary: OID 2662 must have `RelNatts == 1` and one attr — distinct
  from the generic agreement test because pg_class_oid_index is the
  first index PG opens during standby boot and was the FATAL site.

## Verification

- `go test -count=1 -run 'TestNailedIndexRelnattsAgreesWithIndnatts|TestPgClassOidIndexHasSingleKeyColumn|TestPgIndexColDefsMatchesRelcacheAttrs|TestBootstrapPgIndexTuplesWritesHeapPagesToBase1And5' ./internal/initdb/` → PASS
- `go test -count=1 ./internal/initdb/` → 14 pre-existing baseline
  failures (M0106-0012 + the existing TestBootstrappedPGClass /
  PGAttributeRowsReadable etc.) unchanged; baseline diff verified by
  stashing both the modification and the new test file.
- `go test -count=1 ./internal/executor/ ./internal/server/
  ./internal/storage/ ./internal/catalog/ ./internal/mvcc/` → PASS.
