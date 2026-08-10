(idle — nothing in flight)

Last loop: M0119-0006 **23rd slice landed** — the pgoutput logical-replication
decoder learns array columns, and the ArrayType renderer stops having two
copies.

Findings worth carrying:

- Same blind spot as the previous slice, one layer over: a user array column is
  `catalog.Type{Name:<ELEMENT>, IsArray:true}`. `internal/wal/pgoutput.go`
  switched on `Name` alone at THREE seams — `pgoPhysicalAlign`,
  `pgoDecodePhysicalValue`, and the `R` message's `pgoTypeOIDFor`.
- The non-obvious half is the BYTE COUNT, not the value: a `uuid[]` read as a
  scalar uuid consumes 16 bytes instead of the blob's length, so every FOLLOWING
  column decodes from the middle of the array body. A per-value test cannot see
  it — gate the tuple walk (`encodePgoTuplePhysical`) too.
- Structural: `internal/wal` cannot import `internal/executor`, which is exactly
  WHY the support was absent. New leaf package `internal/pgarray` now owns
  `ElemTypeInfo` / `RenderText` / `DecodeElem` / `QuoteTextElem`; the executor
  delegates. Same move as `formatInterval` → `pgdatetime.FormatInterval`. Reach
  for this pattern whenever a heap-codec arm lands — the pgoutput sibling has
  needed a matching edit on every one of the last four slices.
- `catalog.ArrayOIDForBase` already existed (element OID → `_elem` OID); no new
  table was needed for the Relation message.
- `internal/executor/codec_array.go` is NOT gofmt-clean under the local
  go1.26 gofmt at HEAD either (an alignment block in `encodeArrayValuePG`) —
  pre-existing version skew, do not `gofmt -w` it.

Banner state (re-read this loop): M-NIGHTLY's six `20260810-011258` items all
filed AND checked (no newer nightly run); M0130 fully checked; banner falls
through to M0119, then M0122.

Next loop: continue M0119-0006. Fresh candidates from this slice: an end-to-end
subscriber round-trip over a publication on an array column (`internal/testport`),
TOASTed arrays in logical decoding, multi-dimensional / NULL-element arrays
(needs a WRITER first). Older: date/time array elements, array SLICES `a[1:2]`
(rejected by the LEXER), `interval[]` refused by `decodeArrayKeyElemText`,
posting-list duplicate coverage in the checkunique tier, `box`/`int4range` key
encodings, the whole-database (unscoped) pg_amcheck run.

Gates: units PASS; `scripts/tpch-spotcheck.sh` PASS (Q12=2, Q13=35);
`TestPort_RegressSuite` PASS (158s). TPC-DS SF0.5 sweep NOT run — the executor
side of this slice is a pure delegation (same bytes, same renderer) and TPC-DS
has no array columns; the behavior change is confined to `internal/wal`.

In-flight: none
