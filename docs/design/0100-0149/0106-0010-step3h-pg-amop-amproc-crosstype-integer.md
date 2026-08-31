# M0106-0010 Step 3h — Cross-Type `pg_amop` / `pg_amproc` Rows for `integer_ops`

Status: accepted (2026-05-17)

## Background

Step 3g landed a full 21-column `Form_pg_index` row set for every nailed
local + shared index, which unblocked `SearchSysCache1(INDEXRELID, …)` for
PG backends booted from a goopg cluster. The next failure surfaced is
`get_op_btree_interpretation()` returning no strategy match when a query
compares values across integer widths — for example an `int4` indexed
column compared against an `int2` literal — because the bootstrap only
seeded *same-type* `pg_amop` rows for the btree `integer_ops` family
(OID 1976).

`pg_amop.dat` upstream ships six cross-type strategy sets in `integer_ops`:

| left | right | name  |
|------|-------|-------|
| int2 | int4  | int24 |
| int2 | int8  | int28 |
| int4 | int2  | int42 |
| int4 | int8  | int48 |
| int8 | int2  | int82 |
| int8 | int4  | int84 |

Each contributes five strategy rows (`<`, `<=`, `=`, `>=`, `>`). The
matching cross-type cmp procs live in `pg_amproc.dat` (amprocnum=1):
`btint24cmp` (2190), `btint42cmp` (2191), `btint28cmp` (2192),
`btint82cmp` (2193), `btint48cmp` (2188), `btint84cmp` (2189).

Without these rows PG cannot drive an index scan whose key opclass
differs from the comparison constant; the planner either falls back to
a runtime cast (defeating index ordering) or, in stricter code paths,
returns "no btree strategy" and refuses to use the index entirely.

## Change

### `internal/initdb/initdb.go`

`pgAmopInitialEntries` factored the inner `add` helper into a generic
`addPair(family, lefttype, righttype, ops)` and an `add(family, type, ops)`
shorthand for same-type rows. Six new `addPair` calls land 30 cross-type
strategy rows (int24/int28/int42/int48/int82/int84) right after the
same-type int4/int2/int8 rows. Backing capacity bumped from 55 → 85.

Operator OIDs are taken verbatim from `pg_operator.dat`:

```
int24: < 534  <=540  = 532  >=542  > 536
int28: < 1864 <=1866 = 1862 >=1867 > 1865
int42: < 535  <=541  = 533  >=543  > 537
int48: < 37   <=80   = 15   >=82   > 76
int82: < 1870 <=1872 = 1868 >=1873 > 1871
int84: < 418  <=420  = 416  >=430  > 419
```

`pgAmprocInitialEntries` gains six new cross-type cmp rows for
`integer_ops` (amprocnum=1). The cmp proc OIDs come from `pg_proc.dat`:

```
btint24cmp = 2190
btint42cmp = 2191
btint28cmp = 2192
btint82cmp = 2193
btint48cmp = 2188
btint84cmp = 2189
```

Total `pg_amproc` rows: 30 → 36 (cross-type sortsupport / equalimage are
deliberately *not* seeded — upstream `pg_amproc.dat` ships none, and
PG's `LookupOpclassInfo` treats missing optional support procs as a
no-op rather than an error).

### Tests

`internal/initdb/pg_amop_bootstrap_test.go`:

- `TestPgAmopInitialEntriesCoverPinnedOpclasses` drops the
  `e.LeftType != e.RightType` rejection, widens its lookup key to
  `(family, lefttype, righttype, strategy)`, and bumps the expected
  total from 55 → 85. The new cross-type rows are pinned by adding six
  `{1976, l, r, ops, label}` entries to the want table.

`internal/initdb/pg_amproc_bootstrap_test.go`:

- `TestPgAmprocInitialEntriesCoverPinnedOpclasses` total bumped 30 → 36.
- New `TestPgAmprocInitialEntriesCoverCrossTypeInteger` pins each of
  the six cross-type cmp rows by (lefttype, righttype) → proc OID.

## Why other families are left alone

`text` / `name` (family 1994) and the pattern families also ship
cross-type rows in upstream PG, but the immediate failure surfaced by
`TestE2E_FailoverGoopgToPG/async` after Step 3g is the
`get_op_btree_interpretation` no-match on integer cross-type index
scans inside the PG backend's nailed-catalog init path. Adding the
integer cross-types is enough to make standby boot stop FATAL'ing on
this code path; text/name and pattern cross-types are deferred to a
later step (3i) when a concrete blocker appears.

## Verification

- `go test -count=1 -run 'TestPgAmop|TestPgAmproc' ./internal/initdb/` PASS
- `go test -count=1 ./internal/initdb/` — 14 pre-existing baseline
  failures (M0106-0012 + 13 migration/catalog tests confirmed via
  stash-baseline diff to be unchanged by this commit); all pg_amop /
  pg_amproc tests PASS.
- `go test -count=1 ./internal/executor/ ./internal/server/
  ./internal/storage/ ./internal/catalog/ ./internal/mvcc/` PASS.

## Files

- `internal/initdb/initdb.go`
- `internal/initdb/pg_amop_bootstrap_test.go`
- `internal/initdb/pg_amproc_bootstrap_test.go`

## Open items

- Step 3i (next): cross-type `pg_amop` / `pg_amproc` rows for `text_ops`
  (text↔name) and the pattern families if a PG backend startup blocker
  surfaces them.
- `in_range` (amprocnum=3) and `skipsupport` (amprocnum=6) procs are
  still not seeded; upstream ships them for `integer_ops` but they are
  consulted only by window-frame `RANGE BETWEEN` and skip-scan paths
  that the standby-boot critical-index init does not exercise.
