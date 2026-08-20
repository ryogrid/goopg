(idle — nothing in flight)

Last completed: M0134-0038 (json.sql) PARKED with deferral ledger rows
(no code change — bookkeeping-only loop). Sized at HEAD via
`scripts/pg-regress-runner.sh --verbose json`: 3156 raw diff lines, 325
`^+ERROR` / 93 `^-ERROR`, 0/1 PASS. Unlike the prior several M0134 cases,
no CONTAINED bucket existed to ship this loop — every bucket is either a
genuinely missing feature family (REFACTOR-tier) or unsized:
- Bucket 1 (DOMINANT, ~280+ of 325 `^+ERROR`): almost the entire JSON
  constructor/deconstructor builtin family (`to_json`, `json_build_object`,
  `json_build_array`, `json_object`, `row_to_json`, `array_to_json`,
  `json_strip_nulls`, `json_array_length`, `json_object_keys`, ~15+ fns) is
  unimplemented past its pg_proc seed row. `internal/executor/expr.go:11697`
  has exactly one JSON case (the M0134-0037 extract_path family). Needs a
  shared JSON-value internal encoder goopg doesn't have yet. Already flagged
  systemic in M0134-0002's ledger row.
- Bucket 2: table-valued JSON SRFs (json_each, json_array_elements,
  json_populate_record[set]) entirely unimplemented — needs real
  SRF/tupledesc plumbing.
- Bucket 3: parser has NO support for function column-definition lists
  (`AS q(a text, b text)`) — blocks several Bucket-2 SRFs even once built.
  `internal/parser/select.go:1591-1632`.
- Bucket 4 (out of scope): to_tsvector/ts_headline calls incidental to this
  file — full-text-search subsystem, unrelated.
- Bucket 5 (unsized, likely CONTAINED): `::json #> array[...]` parser
  syntax error on `#>` operator token — not root-caused this loop.
- Confirmed: json.sql has ZERO `RETURNS TABLE` plpgsql functions, so
  M0134-0037's Bucket A does not resurface here.

Most-leveraged next slice if this JSON-builtins epic gets its own task:
scalar-only `to_json`/`row_to_json`/`array_to_json` (no SRF, no Bucket-3
parser work needed) — cross-links to the already-ledgered M0134-0002 gap,
but it's net-new implementation (needs a JSON encoder), not a bug fix, so
it should be scoped as its own dedicated task rather than folded into a
"size the next case" loop.

Next loop: per fix_plan.md banner, select M0134-0039 (jsonb.sql, status
`failed`) — same sizing pattern (researcher sizes at HEAD first, confirm
not stale, bucket root causes CONTAINED vs REFACTOR-tier, ship the
smallest CONTAINED bucket or PARK with ledger rows). jsonb.sql likely
shares Bucket 1's missing-JSON-builtin-family root cause with json.sql —
if so, sizing should explicitly cross-reference this row rather than
re-deriving it, and the JSON-builtins epic may be worth promoting to its
own dedicated multi-loop task given it now blocks (at least) two regress
cases.

Gates run this loop: none required — bookkeeping-only PARK, no code
changed. make ralph-state-guard: run before finishing this loop.

In-flight: none.
