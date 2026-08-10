(idle — nothing in flight)

Last loop: M0119-0006 **21st slice landed** — the ARRAY ELEMENT images for
`interval[]` / `uuid[]` / `numeric[]`. One slice closed all three ledger rows
the interval/uuid/numeric COLUMN slices had each left behind.

Findings worth carrying:

- The defect was not "elements are text" but a blob-vs-catalog disagreement:
  `arrayElemTypeInfo` had no arm for the three, so they took the *unknown
  element type* fallback, which stamps `elemtype = 25 (text)` in the ArrayType
  header while `pg_attribute.atttypid` (a different, always-correct mapping in
  `pg18_user_catalog_rows.go`) said `_interval`/`_uuid`/`_numeric`.
- That same header field made the legacy read trivial — no byte analysis like
  the scalar numeric slice needed. The blob STATES its element type, and
  elemtype 25 under one of these columns can only be the pre-flip encoder.
- `pg_column_size` on the reference PG (port 65432, `PGPASSWORD=postgres`) is a
  cheap exact oracle for on-disk element layout: 56/56/44 for a 2-element
  interval/uuid/numeric array pinned align 8 / 1 / 4 without pageinspect
  (which is NOT available on that cluster).
- A round-trip test cannot catch this class — a self-consistent encoder/decoder
  pair round-trips text elements just as happily. `TestArrayCodecPGTypeOnDiskLayout`
  asserts the blob length + elemtype directly against PG's `pg_column_size`.

Banner state (re-read this loop): M-NIGHTLY's six `20260810-011258` items all
filed AND checked; M0130 fully checked; banner falls through to M0119, then
M0122.

Next loop: continue M0119-0006. Highest-value candidates, all fresh ledger
rows from this slice: array SUBSCRIPT still yields `KindString` so
`c[1] = c[2]` over `{'1 mon','30 days'}` is `f` vs PG's `t` (expression
evaluator, not codec); `internal/wal/pgoutput.go` ignores `t.IsArray` so
logical replication decodes ANY array as its scalar element type;
`interval[]` index-key elements refused by `decodeArrayKeyElemText`. Older:
posting-list duplicate coverage in the checkunique tier, `box`/`int4range`
key encodings, the whole-database (unscoped) pg_amcheck run.

Gates run: `RALPH_PRECOMMIT_SCOPE=units scripts/ralph-precommit-test.sh` PASS;
`scripts/tpch-spotcheck.sh` PASS (Q12=2, Q13=35);
`go test -run TestPort_RegressSuite ./internal/testport/` PASS (257s, the
Rule-#5 codec gate); live throwaway server on 5533 reproduced all three PG
renderings end to end; pre-commit pgbench smoke PASS. The TPC-DS SF0.5 sweep
was NOT run: the change is reachable only from `IsArray` columns and TPC-DS
has none, so the sweep would exercise zero changed lines — arrays are covered
by the regress port suite instead.

In-flight: none
