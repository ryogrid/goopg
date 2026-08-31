# M0134-0128 — `hash_func.sql`: scalar hash-function family + `integer::bit(n)` cast

Status: PARKED (`failed`) — two contained fixes landed, 18 hash-function
families deferred (REFACTOR-tier, per-type canonical-encoding work).

## Sizing

`scripts/pg-regress-runner.sh hash_func` against the PG 18.3 oracle:
0% parity, diff **447 → 317 lines** after this loop's fixes (374-line
`expected/hash_func.out`).

`hash_func.sql`'s shape is unusual among M0134 cases: every query is a
self-consistency check —

```sql
SELECT v, hashint4(v)::bit(32) AS standard,
       hashint4extended(v, 0)::bit(32) AS extended0,
       hashint4extended(v, 1)::bit(32) AS extended1
FROM (VALUES (0), (1), ...) x(v)
WHERE hashint4(v)::bit(32) != hashint4extended(v, 0)::bit(32)
   OR hashint4(v)::bit(32) = hashint4extended(v, 1)::bit(32);
```

expecting `(0 rows)` — i.e. it does not pin fixed hash values, only that the
`*extended` sibling's seed=0 low-32-bits agrees with the plain hash and its
seed=1 output differs. **Any internally-consistent implementation of the
extended/non-extended pair passes**, independent of whether it reproduces
PG's exact Jenkins-hash output bit-for-bit (though the implementation here
does, since it reuses `hash_partition.go`'s already-PG-faithful primitives).

## Root cause #1 (blocked every statement): `integer::bit(n)` cast missing

`evalCast`/`evalCastTyped` (`internal/executor/expr.go`) had no `bit`/`varbit`
target-type arm for an integer source at all. `5::bit(32)` silently fell
through to a generic stringify and printed `"5"` instead of PG's 32-digit
binary string — confirmed live before any hash-function work:

```
postgres=# SELECT 5::bit(32);
 bit
-----
 5              -- WRONG (goopg, before fix)
```

vs PG's `bitfromint4`/`bitfromint8` (`postgres/src/backend/utils/adt/varbit.c`
L1531-1608): dest width ≤ source width → copy the low N bits verbatim; dest
width > source width → sign-extend (arithmetic-shift semantics, byte-boundary
bookkeeping in the C source but reducible to plain sign-extension once you
strip the VarBit byte-layout mechanics).

**Fix**: `intToBitTypmodString(val int64, srcWidth, n int) string` — a direct
port of that reduced algorithm — plus a special-case arm in the `CastExpr`
eval site (`internal/executor/expr.go`, right before the `evalCastTyped`
call), because the bit width lives in `x.Typmod` and `evalCastTyped`'s
`(targetType, sourceType string)` signature has no slot for it (mirrors the
existing `x.Typmod`-consuming arms for `varchar`/`numeric`/`time` right below
it in the same function).

This is a general, standalone PG-compat fix — every `hash*()::bit(32)` probe
in this file depends on it, but so does any other `int::bit(n)` cast
anywhere.

## Root cause #2: `pg_proc`-seeded hash functions had zero dispatch

`internal/initdb/pg_proc_seed_data.go` already seeds all 27 base + 27
`*extended` hash-function OIDs (confirmed via grep — every one of
`hashint2`/`hashint4`/`hashint8`/`hashoid`/`hashchar`/`hashfloat4`/
`hashfloat8`/`hashname`/`hashtext`/`hashbpchar`/`hashmacaddr`/`hashmacaddr8`/
`hashinet`/`hash_numeric`/`hash_array`/`hashoidvector`/`hash_aclitem`/
`hashenum`/`time_hash`/`timetz_hash`/`interval_hash`/`timestamp_hash`/
`uuid_hash`/`pg_lsn_hash`/`jsonb_hash`/`hash_range`/`hash_multirange`/
`hash_record` and their extended siblings). But `evalFuncCall`'s big
name-dispatch `switch` (`internal/executor/expr.go`) had no matching `case`
for any of them, so a direct `SELECT hashint4(x)` fell through to the generic
`42883 function hashint4 does not exist`.

Separately, `internal/executor/plpgsql_runtime.go`'s `dispatchInternalFunction`
already has a single `case "hashint8":` (calling `pgHashInt8`) — but that path
only fires for a user-created `LANGUAGE INTERNAL` routine whose body names
`hashint8`, not for a plain SQL call to the builtin. Confirmed live: even
`SELECT hashint8(42);` (the one function with SOME Go implementation already)
raised `42883` before this loop's fix.

**Fix**: `evalHashFunc(name, x, slot, ctx)` (`internal/executor/expr.go`),
wired from ten new `case` labels in `evalFuncCall`'s switch. Reuses
`hash_partition.go`'s already-PG-faithful Jenkins-hash primitives
(`pgHashUint32Extended`/`pgHashBytesExtended`/`pgHashInt8`, ported for
`satisfies_hash_partition` under M0097-0027 + M0134-0071) — this slice is
pure wiring to those primitives, not new hash-algorithm work. Added one
missing primitive, `pgHashUint32` (`hash_partition.go`) — the non-extended
32-bit truncation `hash_bytes_uint32`'s Go equivalent, alongside the existing
`pgHashUint32Extended`.

Families landed (10, all reduce to one of two shapes):

| shape | families |
|---|---|
| fold to a `uint32` key, then `pgHashUint32`/`pgHashUint32Extended` | `hashint2`, `hashint4`, `hashint8` (xor-halves fold, matches `pgHashInt8`'s own derivation), `hashoid`, `hashchar` |
| raw/canonicalized bytes, then `pgHashBytesExtended` | `hashfloat4`/`hashfloat8` (0/-0 short-circuit + NaN canonicalization before hashing the float8 bit pattern, mirroring `hashfunc.c` L140-230), `hashname`, `hashtext`, `hashbpchar` (raw string bytes) |

All ten are STRICT (NULL primary arg → NULL, matching PG's `proisstrict`).

## What's still deferred (18 families)

Every remaining family needs type-specific canonical-byte encoding this loop
didn't build, or (for the composite types) a general runtime
type→hash-function dispatch mechanism goopg doesn't have yet:

- **Canonical fixed-byte encoding needed**: `hashmacaddr`/`hashmacaddr8`/
  `hashinet` (network types), `time_hash`/`timetz_hash`/`interval_hash`/
  `timestamp_hash` (each has its own on-disk layout, not just `hash_any` over
  the Go representation), `uuid_hash`/`pg_lsn_hash`, `hashoidvector` (raw
  Oid-array bytes), `hash_aclitem` (aclitem struct hash).
- **No OID to hash**: `hashenum` hashes the enum LABEL's assigned catalog
  OID (`hashfunc.c` L128-138); goopg's `KindEnum` Datum only carries
  `{SortOrder, Label}` — no OID field to hash.
- **Needs PG's numeric internal representation**: `hash_numeric`/
  `hash_numeric_extended` — PG normalizes NUMERIC's base-10000 digit array
  (strips leading/trailing zero digits, hashes only the non-zero range, XORs
  in `weight`; scale and sign are deliberately excluded from the hash so
  numerics that compare equal but have different display scale still hash
  equal) — `postgres/src/backend/utils/adt/numeric.c` ~L2816-2896. goopg's
  numeric Datum (`internal/executor/datum.go`) isn't stored in that digit/
  weight form; needs a new extraction routine.
- **Needs a general type→hash-proc dispatch mechanism**: `hash_array`/
  `hash_record`/`hash_range`/`hash_multirange` each combine per-element/
  per-field hashes via `hash_combine`/`hash_combine64`, looking up the
  element/field type's own hash proc dynamically (PG's typcache). The only
  existing precedent, `LookupOpClassHashFunc` (`hash_partition.go`), is
  narrowly scoped to HASH-partition opclasses, not a general OID→hash-proc
  registry.
- **Needs jsonb structural walk**: `jsonb_hash`/`jsonb_hash_extended` (not a
  flat byte hash over the serialized form).

## Adjacent, out-of-scope finding

`-'NaN'::float4` / `-'NaN'::float8` (unary minus applied to a float-cast NaN
literal) raises a spurious `operator unary - requires integer or numeric`.
Surfaced by this file's float special-case probes
(`SELECT hashfloat4('NaN'::float4) = hashfloat4(-'NaN'::float4);`) but is a
pre-existing, unrelated evaluator bug — not diagnosed or fixed this loop.

## Resume points

1. Composite/typed hash functions: extend `evalHashFunc`'s `switch base`
   (`internal/executor/expr.go`) with each remaining case. The
   canonical-byte-encoding families (macaddr/macaddr8/inet/time/timetz/
   interval/timestamp/uuid/pg_lsn/oidvector) follow the same
   `bytesResult`/`uint32Result` helper pattern already in place.
2. Before attempting `hash_array`/`hash_record`/`hash_range`/
   `hash_multirange`: design a small generic "hash proc lookup by type OID"
   helper, parallel to `LookupOpClassHashFunc`.
3. `hash_numeric`: port PG's digit-normalization loop
   (`postgres/src/backend/utils/adt/numeric.c` ~L2816-2887) against whatever
   digit-array accessor the big-numeric path in `internal/executor/datum.go`
   already exposes.
4. Unary-minus-on-NaN: needs its own standalone repro
   (`SELECT -'NaN'::float8;`) before diagnosing.

## Verification

- `go build ./...`
- `go test ./internal/executor/... ./internal/parser/...` — includes new
  `TestHashFuncScalarFamily` (13 subtests) and `TestIntToBitTypmodCast`
  (`internal/executor/hash_func_scalar_test.go`)
- `scripts/tpch-spotcheck.sh` — PASS (Q12=2, Q13=35)
- `RALPH_PRECOMMIT_SCOPE=units scripts/ralph-precommit-test.sh` — PASS
- `scripts/pg-regress-runner.sh hash_func` — diff 447→317 lines, zero
  regressions among the ten landed families
