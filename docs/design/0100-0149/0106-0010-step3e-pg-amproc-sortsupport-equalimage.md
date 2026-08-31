# M0106-0010 Step 3e — Seed sortsupport + equalimage rows in pg_amproc

Status: Accepted (2026-05-17)

## Background

Step 3c bootstrapped `pg_amproc` with the eight default cmp (amprocnum=1)
support functions for the pinned btree opclasses. Step 3d extended the set
to 11 cmp rows after the pinned-opclass family OIDs were corrected (char,
oidvector, bpchar_pattern).

Step 3d's closing note flagged three follow-ups as still open:

1. sortsupport rows (amprocnum=2)
2. equalimage rows (amprocnum=4)
3. cross-type rows where lefttype ≠ righttype (e.g. name × text)

Step 3e closes the first two. Cross-type entries remain deferred — they
require additional cross-type cmp procs (e.g. `btnametextcmp`,
`bttextnamecmp`) and are not needed for hot-standby boot.

## Why sortsupport + equalimage matter

For PG18 hot-standby boot, `LookupOpclassInfo` in `relcache.c` scans
`pg_amproc` for `(opcfamily, opcintype, opcintype)` and caches every
matching row in the relcache opclass entry. Missing cmp rows would PANIC
when an index dispatches its comparison function; missing sortsupport or
equalimage rows are silently tolerated but downgrade runtime behaviour:

- **sortsupport (amprocnum=2)** — without it, every sort using a pinned
  opclass falls back to the slow cmp-only fast-path. Quicksort still
  works, but per-comparison overhead is ~3× higher and TPCH-style
  ORDER BY queries get materially slower.
- **equalimage (amprocnum=4)** — without it, btree page deduplication
  is disabled for any index whose opclass goopg pinned. Storage
  amplification on update-heavy workloads (pgbench-style) regresses.

Seeding these rows is cheap (additional ~190 bytes on disk per database
directory) and brings a goopg-bootstrapped data directory to parity with
a real PG cluster started by upstream `initdb`.

## Rows added

Per PG18 `postgres/src/include/catalog/pg_amproc.dat` for the 11 pinned
default-type opclass families:

| family            | type   | sortsupport         | equalimage          |
|-------------------|--------|---------------------|---------------------|
| integer_ops 1976  | int2   | btint2sortsupport 3129 | btequalimage 5051 |
| integer_ops 1976  | int4   | btint4sortsupport 3130 | btequalimage 5051 |
| integer_ops 1976  | int8   | btint8sortsupport 3131 | btequalimage 5051 |
| oid_ops 1989      | oid    | btoidsortsupport 3134  | btequalimage 5051 |
| text_ops 1994     | text   | bttextsortsupport 3255 | btvarstrequalimage 5050 |
| text_ops 1994     | name   | btnamesortsupport 3135 | btvarstrequalimage 5050 |
| text_pattern 2095 | text   | bttext_pattern_sortsupport 3332 | btequalimage 5051 |
| bool_ops 424      | bool   | — (none in PG18)    | btequalimage 5051 |
| char_ops 429      | char   | — (none in PG18)    | btequalimage 5051 |
| oidvector_ops 1991| oidvec | — (none in PG18)    | btequalimage 5051 |
| bpchar_pattern 2097| bpchar| btbpchar_pattern_sortsupport 3333 | btequalimage 5051 |

Net row count for `pg_amproc`:

- Step 3c → 8 rows
- Step 3d → +3 rows (pinned-opclass cmp) = 11
- Step 3e → +8 sortsupport + 11 equalimage = 30 total

## Out of scope

- Cross-type cmp procs (`btnametextcmp`, `bttextnamecmp`, etc.).
- `in_range` (amprocnum=3) — only used by window-function range frames.
- `skipsupport` (amprocnum=6) — only used by btree skip-scan in 18.x.
- Seeding the new sortsupport/equalimage support procs themselves into
  `pg_proc`. They are not invoked during standby boot; lazy resolution
  via `OidFunctionCall1(PROCOID, oid)` at first use is fine because no
  user query on a goopg-bootstrapped standby will fire before normal
  catalog DML lands those rows. Tracked under M0106-0011 (operational
  maintenance) and the broader pg_proc completion sub-milestone.

## Test pins

- `TestPgAmprocInitialEntriesCoverPinnedOpclasses`
  - amprocnum restricted to {1, 2, 4}; entry count 11 → 30.
- `TestPgAmprocInitialEntriesCoverSortsupportAndEqualimage` (new)
  - pins every (family, lefttype, num) → proc OID combination above.

## Files

- `internal/initdb/initdb.go` — extended `pgAmprocInitialEntries` literal.
- `internal/initdb/pg_amproc_bootstrap_test.go` — relaxed amprocnum
  guard, bumped entry count, added the new coverage pin.
