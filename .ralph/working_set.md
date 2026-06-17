(idle — nothing in flight)

Last landed: DU-002 slice 130 (loop #95) — per-sequence CACHE size now
round-trips through pg_dump. goopg parsed `CACHE n` on CREATE/ALTER SEQUENCE
but DISCARDED the value: in-memory `seqState` had no cache field and
`sequenceParamsForCatalog` hard-wired `Cache: 1` into pg_sequence.seqcache, so
every dumped CREATE SEQUENCE emitted `CACHE 1` regardless of the declared cache.
The ALTER path was the sibling: parser parsed `CACHE n` into a throwaway local.
Fix (work was completed by a usage-limit-cut-off prior loop; this loop verified
+ committed it):
  - executor/operators_sequence.go: seqState.cache int64 (default 1 in
    RegisterSequence); new SetSequenceCache(name, cache) (clamps <1 to 1);
    UpdateSequenceParams gains cache *int64; sequenceParamsForCatalog returns
    tracked value (default-1 guard for pre-tracking sequences).
  - executor/operators_ddl.go: execCreateSequence calls SetSequenceCache when
    CACHE n given; ALTER path threads s.Cache into UpdateSequenceParams.
  - parser/ast.go: AlterSequenceStmt.Cache *int64
  - parser/ddl.go: ALTER `cache` case stores parsed value (was discarded)
  CREATE SEQUENCE public.cache_seq CACHE 5;  → … CACHE 5;
  ALTER SEQUENCE public.altcache_seq CACHE 42; → … CACHE 42;
Verified byte-identical to pg_dump 18.3; TestPort_PgDumpConnectionSetup PASS.
Committed + pushed.

Next direction (slice 131): a table+VIEW dependency-ordering case (verify
topological emission ORDER — view depends on a table, must be dumped after), OR
a UNIQUE constraint with an INCLUDE column.
