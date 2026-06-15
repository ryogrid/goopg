Task: M0110-0001 / DU-002 — pg_dump catalog-view parity. Slice 33 COMPLETE
this loop (commit pending). NOTHING in flight; next loop starts on slice 34
(pg_proc.proretset column for getFuncs/dumpFunc).

=== DONE (loop #56) — DU-002 slice 33 ===
`current_schemas(boolean)` now returns a parseable `{a,b}` name[] text array
literal instead of a bare scalar. pg_dump's selectDumpableNamespace runs
`SELECT pg_catalog.current_schemas(false)` → parsePGArray → previously aborted
"could not parse result of current_schemas()".
Fix is EXECUTOR-ONLY (internal/executor/expr.go):
- new shared searchPathSchemas(ctx) resolver: existing search-path schemas in
  order (pg_catalog/information_schema/public always exist; user schemas via
  ctx.Catalog.LookupTable).
- current_schema → scalar first entry (currentSchemaFromSearchPath refactored
  to use the resolver; behavior preserved: default → "public").
- current_schemas(include_implicit) → currentSchemasArray: renders `{a,b}`,
  prepends pg_catalog when include_implicit=true. Dispatch split out from the
  shared `current_schema` case (was `case "current_schema","current_schemas"`).
pg_proc already declares rettype 1003 (name[]) → NO catalog change needed.
Files: internal/executor/expr.go (dispatch + 3 helpers),
internal/executor/current_schemas_test.go (NEW unit guard),
internal/testport/pgdump_connsetup_test.go (header→next blocker),
docs/design/0110-0001-pg-dump-tap-port.md (slice 33 entry + next blocker).
Gates: build clean; gofmt/vet clean (my files); TestCurrentSchemasArrayLiteral
PASS; TestPort_PgDumpConnectionSetup PASS (pg_dump advanced past
current_schemas). tpch-spotcheck N/A (additive scalar-builtin parity; no
physical/codec/executor-semantics change).

=== NEXT STEP — DU-002 slice 34 (pg_proc.proretset) ===
pg_dump now fails: `column "proretset" does not exist` (EXECUTE
dumpFunc('1654')). getFuncs/dumpFunc prepared query reads pg_proc.proretset
(the returns-set boolean flag). goopg's pg_proc virtual view does not expose it.
FIRST: find the pg_proc virtual view column definitions (internal/catalog or
internal/initdb pg_proc_seed_data.go / replication_views-style builder) and add
proretset bool. For built-in procs, proretset = true iff the function is a SRF
(generate_series, unnest, etc.); for the dump probe a constant 'f' may suffice
if user functions aren't SRFs — verify what dumpFunc filters on.
RUN TestPort_PgDumpConnectionSetup (-count=1 -v) to confirm + find next blocker.

ORTHOGONAL PRE-EXISTING (track separately): reading a text[] column back from
the heap yields the BINARY array encoding (KindString raw bytes), not the text
repr expandArrayDatum parses. NOT hit by slice 33 (current_schemas builds the
literal directly).

Other open (larger, untouched): M0110-0003 AC-003 003_check feature tiers;
M0110-0002 002_save_fullpage; M0095-0003 recvlogical; M0117-0006/7/8 (CLOG).
