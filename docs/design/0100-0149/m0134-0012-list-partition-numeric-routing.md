# M0134-0012 — LIST partition routing drops every non-int/string/bool key kind

Status: LANDED (2026-08-20)
Milestone: M0134 (regress-sql `failed` digestion) — case `update.sql`
Related: `docs/design/m0134-0011-join-on-sublink-catalog.md` (same loop shape:
case PARKED, one contained engine fix shipped out of the sizing round)

## Symptom

```sql
CREATE TABLE list_parted (a numeric, b int, c int8) PARTITION BY LIST (a);
CREATE TABLE list_part1 (...);
ALTER TABLE list_parted ATTACH PARTITION list_part1 FOR VALUES IN (2,3);
INSERT INTO list_parted VALUES (2,5,50);
-- PG 18.3: routes to list_part1
-- goopg:   ERROR: no partition of relation "list_parted" found for row  (23514)
```

The initial suspicion — that multi-value `FOR VALUES IN (2,3)` bound storage only
honoured the first value — was **refuted**. Bound storage is correct and
per-value on both DDL paths (`ATTACH PARTITION`,
`internal/executor/operators_ddl.go:8926`; `CREATE TABLE ... PARTITION OF`,
`:4982`), matching PG's flattened one-entry-per-value representation
(`postgres/src/backend/partitioning/partbounds.c:create_list_bounds`, which sets
`all_values[j].value` / `all_values[j].index` per literal).

## Root cause

`routeToPartitionDepth`'s `case "LIST":` arm
(`internal/executor/operators_storage.go:2934-2960`) stringifies the routing key
with an if/else chain covering exactly four Datum kinds — `KindInt`,
`KindString`, `KindBool`, and null — **with no `default` arm**. `list_parted.a`
is `numeric`, and `coerceRowForConstraintChecks` (`:2353`, called at `:2463`
before routing) has already coerced the literal to a `KindNumeric` Datum. So
`keyStr` stays the empty string, `im.FindPartitionForValue(parent.OID, "")`
(`internal/catalog/catalog.go:4793`) matches nothing against
`InValues=["2","3"]`, and — `list_parted` having no DEFAULT partition — the
caller raises `23514` at `:2536`.

The failure is silent and total: with a `numeric` (or `float`, `date`, `uuid`, …)
LIST key, **no row can ever be inserted**.

## Why it survived: a sibling-path divergence

Three siblings format a partition key for the same
`FindPartitionForValue` string comparison, and only one is wrong:

| site | numeric handled? |
|---|---|
| `routeToPartitionDepth` `case "RANGE":` (`:2966-2985`) | yes — explicit `KindNumeric` case **and** a `default: d.Format()` fallback |
| `partitionKeyDatumToListStr` (`:3211`, used by `routePartitionKeyToImmediateChild` for the DEFAULT-partition anti-siphon check) | yes — `default: d.Format()` |
| `routeToPartitionDepth` `case "LIST":` | **no — closed if/else, no default** |

`partitionKeyDatumToListStr`'s own doc comment says it "mirrors the LIST arm of
`routeToPartitionDepth`" — the mirror had drifted ahead of the original. This is
another instance of the standing `pattern_sibling_paths_must_agree` failure mode
(encode↔decode, fast-path↔interpreted evaluator, …): a green test on one twin
proved nothing about the other, and the *correct* twin was the copy.

## Fix

Delete the duplicated if/else and call the already-correct helper:

```go
case "LIST":
    keyStr := partitionKeyDatumToListStr(keyDatum)
    child = im.FindPartitionForValue(parent.OID, keyStr)
    // …existing short-boolean "t"/"f" retry, unchanged…
```

The helper is a strict superset of the deleted logic for the four kinds it
covered (int/string/bool/null are byte-identical), so the change is
purely additive in behaviour: kinds that previously produced `""` now produce
`Format()`. The short-`"t"`/`"f"` boolean retry stays where it is — the helper
returns only the long form, and the retry is the fallback for bounds spelled
`FOR VALUES IN ('t')`.

Structurally the LIST arm now matches RANGE (both delegate/fall back rather than
enumerating), which is what stops the drift from recurring.

## PG oracle

- `postgres/src/backend/partitioning/partbounds.c:create_list_bounds` — one
  flattened, sorted `PartitionListValue` entry per literal, each with an `index`
  back-pointer to its owning partition.
- `postgres/src/backend/partitioning/partbounds.c:partition_list_bsearch` —
  binary search over that array, reached from `get_partition_for_tuple` (`:3073`).

PG compares bounds as **typed Datums via the partition key's btree comparison
proc**; goopg compares **formatted strings**. For the integral literals in
`update.sql` the two agree, but they diverge whenever two spellings denote one
value (`2` vs `2.0` vs `2.00` for `numeric`; `'01:00'` vs `'01:00:00'` for
`time`). That fidelity gap is orthogonal to this fix and is recorded in the
deferral ledger rather than fixed blind.

## Scope NOT taken

- The string-vs-Datum bound comparison above (ledgered).
- The `HASH` arm at `:2999` has the same closed-if/else shape and is
  UNVERIFIED-but-suspect; not touched without a reproducer (ledgered).
- `update.sql` itself remains `failed` — sizing bucket 1 (multi-level
  partition row routing through column-reordered intermediate partitions,
  ~300 of 841 diff lines) is REFACTOR-tier. The CSV row is unchanged and
  `make regen-testport` is NOT run.
