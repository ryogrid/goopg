# M0133-S2 — `information_schema` helper functions (11 `pg_proc` rows + `prosqlbody`)

**Status:** accepted — landed 2026-08-13.
**Milestone:** M0133 (`information_schema` on disk), slice S2.
**Supersedes:** S9.4b in `0131-0009-system-view-corpus-widening.md` §"Successor decomposition".

## What landed

The **11 helper functions** `information_schema.sql` creates before its domains and
views, seeded into goopg's on-disk `pg_proc` (1255) with their full 30-column rows.
Ten of the eleven are new-style SQL-standard bodies — `prosrc = ''` and a non-null
**`prosqlbody`** `pg_node_tree` — captured verbatim from a throwaway PG 18.3 into
`<name>_prosqlbody.dat` blobs. A real PG 18.3 cold-started on a goopg `$PGDATA` can
now resolve `information_schema._pg_char_max_length` (and its ten siblings) through
`FuncnameGetCandidates`, which the 65 views' rewrite rules depend on.

## Measured object graph

`pg_proc` rows for `pronamespace = 13273`, measured against a fresh PG 18.3
(post-bootstrap counter, not a `.dat` file — the same reason S1's domain OIDs are
not contiguous):

| OID | name | prorettype | proargtypes | prosrc | prosqlbody |
|---|---|---|---|---|---|
| 13274 | `_pg_expandarray` | 2249 (record) | `{2277}` (anyarray) | 51 B `unnest($1) WITH ORDINALITY` | NULL |
| 13275 | `_pg_index_position` | 23 (int4) | `{26,21}` (oid,int2) | `''` | 4150 B |
| 13276 | `_pg_truetypid` | 26 (oid) | `{75,71}` (pg_attribute,pg_type) | `''` | 1814 B |
| 13277 | `_pg_truetypmod` | 23 | `{75,71}` | `''` | 1814 B |
| 13278 | `_pg_char_max_length` | 23 | `{26,23}` (oid,int4) | `''` | 3850 B |
| 13279 | `_pg_char_octet_length` | 23 | `{26,23}` | `''` | 6373 B |
| 13281 | `_pg_numeric_precision` | 23 | `{26,23}` | `''` | 6148 B |
| 13282 | `_pg_numeric_precision_radix` | 23 | `{26,23}` | `''` | 3535 B |
| 13283 | `_pg_numeric_scale` | 23 | `{26,23}` | `''` | 4112 B |
| 13284 | `_pg_datetime_precision` | 23 | `{26,23}` | `''` | 5963 B |
| 13285 | `_pg_interval_type` | 25 (text) | `{26,23}` | `''` | 2621 B |

All eleven: `pronamespace = 13273`, `proowner = 10`, `prolang = 14` (SQL),
`procost = 100`, `proisstrict = t`, `prokind = 'f'`, `prosecdef`/`proleakproof` false,
`proparallel = 's'` except `_pg_index_position` (`'u'` — it is STABLE, not declared
SAFE), `provolatile = 'i'` except `_pg_index_position` (`'s'` STABLE). `proargnames`
is non-null only for the functions whose signature names its arguments —
`_pg_char_max_length`/`_pg_char_octet_length`/`_pg_numeric_precision`/
`_pg_numeric_precision_radix`/`_pg_numeric_scale`/`_pg_datetime_precision`
(`{typid,typmod}`), `_pg_interval_type` (`{typid,mod}`), and `_pg_expandarray`
(`{"",x,n}` — the SRF-with-OUT-args form, `proretset = t`, `prorows = 100`,
`prosupport = 3996` = `array_unnest_support`, `proallargtypes = {2277,2283,23}`,
`proargmodes = {i,o,o}`).

**F1 (this slice) — OID 13280 is a hole.** The functions occupy
`{13274..13279, 13281..13285}`; no `pg_type`/`pg_class`/`pg_proc`/`pg_namespace`/
`pg_operator`/`pg_opclass`/`pg_rewrite`/`pg_constraint` row has OID 13280. It is an
unassigned bootstrap-counter value, not a helper function, so S2 seeds nothing there
(the fix_plan's "13274..13285" is inclusive of the hole, not an eleventh+twelfth
function). S4 must not assume the band is dense.

## Design

### 1. Capture — `scripts/capture-ev-action.sh --prosqlbody`

A second capture surface, exactly as F34 predicted: the view corpus captures
`pg_rewrite.ev_action`; these functions carry their node tree in `pg_proc.prosqlbody`.
The mode reuses the throwaway-oracle cluster setup and, for each function OID, emits:

- a `<name>_prosqlbody.dat` blob — the verbatim `prosqlbody` node tree, one line, no
  trailing newline — for every function with a non-null prosqlbody (10 of 11);
- an `information_schema_proc_manifest.tsv` `proc` row carrying the full 19-field row
  (name, oid, pronamespace, procost, prorows, prosupport, prokind, provolatile,
  proparallel, proisstrict, proretset, prolang, prorettype, proargtypes,
  proallargtypes, proargmodes, proargnames, prosrc, prosqlbody length-or-`-`).

`--prosqlbody --verify` re-derives the committed set from the manifest and byte-`cmp`s
the blobs + manifest against the tree. Two guards keep the reduced manifest honest:
the capture fails if any *unmodelled* column (prosecdef, proleakproof, proowner,
provariadic, pronargdefaults, protrftypes, probin, proconfig, proacl) is non-default,
and if prosrc contains a tab/newline (which would split the TSV).

Unlike `ev_action`, the blob's outer node is **not** asserted to be parenthesised: a
single-statement `RETURN <expr>` body nodeToString's to `{QUERY ...}` while
`_pg_index_position`'s `BEGIN ATOMIC` body is a parenthesised List.

### 2. Generation — `cmd/gen-information-schema-procs`

A dedicated generator (rather than extending `cmd/gen-nailed-view-tables`) because the
two manifests have disjoint schemas (rel/attr vs proc row) and that generator owns a
single stdout stream; the `.dat` + manifest + render-into-Go shape is identical. It
parses the manifest (including a PG-array-literal parser for `proallargtypes`/
`proargmodes`/`proargnames`) and renders `informationSchemaHelperProcs()` into
`internal/initdb/information_schema_proc_seed.go`. The prosqlbody node tree is **not**
emitted into Go — it is resolved at runtime through `nailedProcSqlBody` (capture, do
not generate, exactly the ev_action convention).

### 3. Seed wiring

- `pgProcEntry` gained `Namespace`, `Cost`, `Rows`, `Support`, `SqlBody` (zero values
  preserve the pre-existing hardcoded defaults, so the 3397 `pg_proc.dat` entries are
  unchanged).
- `pgProcRow` emits them: `pronamespace` from `Namespace` (default 11), `procost` from
  `Cost` (default 1), `prorows`/`prosupport` verbatim, and `prosqlbody` from
  `nailedProcSqlBody(SqlBody)` when `SqlBody != ""` (else NULL).
- `pgProcInitialEntries()` appends `informationSchemaHelperProcs()` to the 3397
  `.dat` entries (→ 3408), so `bootstrapPgProcTuples` and both `pg_proc` index
  bootstraps pick them up unchanged.

### 4. The namespace index fix (sibling path)

`bootstrapPgProcPronameArgsNspIndex` hardcoded `nsp = 11` for every row. That is the
`pg_proc` twin of the `pg_type_typname_nsp_index` gap S1 fixed (0133-0001 F1): a
hosted PG's `FuncnameGetCandidates` for `information_schema._pg_char_max_length(...)`
searches PROCNAMEARGSNSP keyed on `(proname, proargtypes, 13273)`, and an 11 there
made the lookup miss with 42883. The fix reads `e.Namespace` (default 11), and a new
test asserts the 13273-keyed tuple is present and the 11-keyed one is absent.

## Guards

- `TestInformationSchemaHelperProcsMatchManifest` — the generated entries equal the
  oracle capture field-by-field (non-vacuous: exactly 11).
- `TestInformationSchemaProcSqlBodyBlobsMatchManifest` — the embedded blob set equals
  the manifest's non-null-prosqlbody set, and every blob round-trips.
- `TestBootstrapPgProcPronameArgsNspIndexNamesInfoSchemaNamespace` — the index keys
  the helpers under 13273, not 11.
- `TestPgProcSeedLeavesAbsentVarlenaAttrsNull` updated: `prosqlbody` may now be
  non-null only for the 10 helpers that name a `SqlBody`, and must be a genuine node
  tree there, never an empty shell.
- `TestPgProcInitialEntriesCoverAMHandlers` count 3397 → 3408.

Gates: `internal/initdb` PASS (224 s), `internal/catalog` PASS,
`scripts/capture-ev-action.sh --prosqlbody --verify` PASS (11 procs byte-identical),
`go build ./...` clean, `go vet ./internal/initdb` clean.
