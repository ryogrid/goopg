(idle — nothing in flight)

Last loop (#90): M0119-0006 25th slice — **array index-key DECODABILITY**.
Design `docs/design/0119-0006-array-index-key-decodability.md`, index row
`0119-0006s`, 3 ledger rows (one of them a RETRACTION).

M-NIGHTLY duty this loop: `ci/logs/action-items.md` is still nightly run
`20260811-014635` (12 items), ALL filed by loop #87 — nothing new to add.
Eleven remain PARKED per banner; re-run their repros at HEAD before
investigating.

What landed + what it FOUND: `indexOnlyScanOp.indexKeyIsDecodable` tested
`!col.Type.IsArray && isIntervalTypeName(...)`, so an `interval[]` column — whose
element key is the SAME non-invertible `interval_cmp_value` span — was reported
decodable and an IOS over an ALL_VISIBLE page failed the whole statement:
`SELECT i FROM av WHERE i = '{3 days}'` → `XX000: IOS decode: btree: interval key
is the comparison span …`. Same for date[]/time[]/timetz[]/timestamp[]/
timestamptz[]/bytea[]. Now answered by the key layer:
`internal/executor/btree_key_decodable.go::indexKeyColumnIsDecodable` recurses
into the element as `decodeArrayBTreeKey` does, over `arrayKeyElemRenderer`
(rendering table lifted out of `decodeArrayKeyElemText`, nil = refused). The
refused types STAY refused and the old ledger row's proposed `formatInterval`
arm is retracted as wrong (the span kept no month/day split; PG calls `1 mon` =
`30 days`). Second, independent drift found by the parity gate: `uuid` was in
`decodeIndexKeyColumn`'s text-like case but NOT in `decodeBTreeKeyToDatum`'s, so
the single-column lane fell to the `default:` arm that reads 8 bytes as an enum
sort order and never errors — empty Datum for a real uuid (latent: uuid indexes
take the PG tuple-image key path).

Banner state: M-NIGHTLY filing done; M0130 fully checked; banner falls through to
M0119 (M0119-0005 blocked on missing hash/gin/gist/spgist/brin AMs, so
M0119-0006 is the actionable head), then M0122.

Next loop: per banner, M0119-0006 again. Remaining named in fix_plan:
`box`/`int4range` key encodings (both types unsupported in goopg entirely) and
the whole-database unscoped pg_amcheck run. Also open: array SLICES `a[1:2]`
(rejected by the LEXER), heap element images for date/time/timestamp/bytea/enum
arrays (what keeps those refused as index-key elements), TOASTed / multi-dim /
NULL-element arrays in logical decoding, a subscriber round-trip E2E over a
publication on an array column.

Gates: build + vet clean; `go test ./internal/executor/ ./internal/amcheck/
./internal/access/btree/` PASS; units (`RALPH_PRECOMMIT_SCOPE=units
scripts/ralph-precommit-test.sh`) PASS; `scripts/tpch-spotcheck.sh` PASS
(Q12=2, Q13=35); pgbench smoke via the commit hook. Three new gates each
mutation-checked.

In-flight: none
