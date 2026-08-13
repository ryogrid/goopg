(idle — nothing in flight)

M0133-S3 DONE + committed: the 4 `information_schema` data tables — `sql_features`
(755) / `sql_sizing` (23) / `sql_implementation_info` (12) / `sql_parts` (11), 801
rows — seeded as ordinary heaps, each a five-OID object graph (table T / array
T+1 / composite T+2 / toast heap T+3 / toast index T+4; 13456..13475). First
bulk-heap-load-at-initdb mechanism: `informationSchemaDataTableRels()` (a THIRD rel
list — in pg_class/pg_attribute/pg_type content, never pg_internal.init) +
`nailedToastPairs()` reuse for the empty TOAST pairs + 8 `pgTypeCanonical` cases +
`//go:embed`-ed TSV data through `writeMultiPageHeapRows`. Columns are the S1
domains; heap encoder uses base type names (`text`/`int4`) while pg_attribute
carries the domain OIDs (load-bearing split, F3). Design `0133-0003`.

Ledgered F4 (blocks S4): domain-typed expressions (`feature_id = 'x'`, `||`,
`concat`) fail `operator is not unique: information_schema.character_data = unknown`
on a hosted PG though operators/casts/typbasetype are all on disk — PG's own
`oper()` domain→base reduction diverges; triage before S4's WHERE-clause views.

Next per banner (M0133, S3 done → S4): **M0133-S4 — the 65 views** (runs LAST),
reusing the M0131-S9 capture/pin/regen loop; re-point `assertNonCorpusSystemViewIsStillAbsent`
(the `information_schema.tables` tripwire flips red).
