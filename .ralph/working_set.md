(idle — nothing in flight)

M0133-S2 DONE + committed: the 11 `information_schema` helper functions
(`pg_proc` OIDs 13274..13279 + 13281..13285; **13280 is a hole**, unassigned).
10 carry `prosrc=''` + a non-null `prosqlbody` captured verbatim into
`<name>_prosqlbody.dat`; `_pg_expandarray` is textual (prosrc 51 B, SRF with
OUT args, prosupport=3996 `array_unnest_support`).

New capture surface `scripts/capture-ev-action.sh --prosqlbody [--verify]` +
generator `cmd/gen-information-schema-procs` (separate from gen-nailed-view-tables:
disjoint schema, one stdout stream) → `informationSchemaHelperProcs()` +
`internal/initdb/information_schema_proc_manifest.tsv`.

`pgProcEntry` gained `Namespace/Cost/Rows/Support/SqlBody`; `pgProcRow` emits
them; `pgProcInitialEntries` 3397→3408. Sibling-path fix forced:
`bootstrapPgProcPronameArgsNspIndex` hardcoded `nsp=11` (the pg_proc twin of
S1's `pg_type_typname_nsp_index` gap) → now reads `e.Namespace`.

Design `0133-0002` + README row. Gates: `internal/initdb` PASS (224 s),
`internal/catalog` PASS, `--prosqlbody --verify` byte-identical (11 procs),
UNITS PASS, `go build`+`go vet` clean.

Next per banner (M0133, S2 done → S3): **M0133-S3 — the 4 data tables + 801
rows** (F35). `sql_features` (755) / `sql_sizing` (23) / `sql_implementation_info`
(12) / `sql_parts` (11) are ordinary heaps; upstream initdb COPYs them from
`sql_features.txt`. Needs a bulk-heap-load-at-initdb mechanism M0131-S9 never
produced — write its own design section before code. Gates: a hosted PG reading
real rows out of `sql_features`, not just planning it.
