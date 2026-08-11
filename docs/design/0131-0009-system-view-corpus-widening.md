# System-view corpus widening — take the six-view island to `system_views.sql` scale

**Status:** draft
**Date:** 2026-08-11
**Milestone:** M0131 (S9)

## Problem

### goopg's system views are virtual, and virtual means absent from disk

`catalog.Table` carries a `Virtual bool` + `VirtualRows func() [][]string` pair
(`internal/catalog/catalog.go:335-342`). The planner short-circuits a scan of
such a table into a materialised `Values` node; the runtime side is
`rematerialiseVirtualRows` (`internal/executor/operators.go:61-63`), which calls
`tbl.VirtualRows()` and builds one `planner.Expr` cell per column. Rows are
computed on demand and never touch a heap file.

Counting them (method: `grep -c 'Virtual: *true'` over non-test Go sources):
**91** in `internal/catalog/catalog.go`, registered by `registerSystemTables()`
(`:7980`) and `registerInformationSchemaTables()` (`:11603`) — of the `Schema:`
literals in that file 85 are `"pg_catalog"` and 7 `"information_schema"` — plus
**24** across `internal/initdb/` (`replication_views.go` 11, `aio_views.go` 3,
`pg_stat_ssl_gssapi_view.go` 2, `pg_aggregate_view.go` 2, one each in
`wal_io_views.go`, `pg_stat_activity_view.go`, `pg_sequences_view.go`,
`pg_proc_view.go`, `information_schema_sequences_view.go`, `open.go`),
installed by the 19 distinct `register…View` functions called from
`internal/initdb/open.go:1885-2260`. **~115 relations** (the plan estimates
~118; the delta is counting method — one `analyzer.go` hit is excluded as not a
system catalog).

None has a `pg_class` heap row. `PGClassRowsForDBOid` skips them explicitly
(`internal/catalog/catalog.go:7026-7034`):

```go
if t.Virtual && t.View == nil && !t.IsMatView && !t.IsSequence {
    // Skip system-catalog virtual tables (pg_class, pg_locks, etc.)
    // but include user views (t.View != nil), materialized views, and
    // user sequences (relkind='S'). ...
    continue
}
```

### The failure mode is 42P01, not 42809 — and that distinction is the slice

For the six views that *do* have on-disk `pg_class` + `pg_attribute` rows
(`relcache_init.go:688`, `:693-697`), a hosted PG resolves the name, opens the
relation, finds no rules, and raises **42809 `… is not a table`** from
`plancat.c:139-149`. That is M0131-S5/S6's problem: the relation exists, the
rule path is blind.

For the other ~115, `RangeVarGetRelid` returns `InvalidOid` and the query dies
with **42P01 `relation "pg_stat_activity" does not exist`** — the same error
string recorded for the very first nailed view
(`relcache_init.go:665-668`). Nothing in S5 or S6 moves this. The six views are
an *island*; S5/S6 make the island habitable, S9 is the only slice that grows
it. A reader who assumes "S6 fixes views on a hosted PG" is wrong by ~115
relations.

## Design

### The `pgnodes` non-path

goopg has a `pg_node_tree` IR (`internal/pgnodes`) that round-trips `Query`
trees and already emits canonical `ev_action` for **user** views. Pointing it at
`system_views.sql` is tempting and structurally impossible.

- **Node-tag coverage: 21 tags.** From the `case "…"` labels in `readfuncs.go` +
  `readfuncs_query.go`: `ALIAS`, `BOOLEANTEST`, `BOOLEXPR`, `CASEEXPR`,
  `CASETESTEXPR`, `CASEWHEN`, `COERCEVIAIO`, `CONST`, `DISTINCTEXPR`,
  `FROMEXPR`, `FUNCEXPR`, `NULLTEST`, `OPEXPR`, `QUERY`, `RANGETBLENTRY`,
  `RANGETBLREF`, `RELABELTYPE`, `RTEPERMISSIONINFO`, `SQLVALUEFUNCTION`,
  `TARGETENTRY`, `VAR`. No `RangeTblFunction`, `JoinExpr`, `Aggref`, `SubLink`,
  or `ArrayExpr`.
- **The reader hard-rejects everything but a plain relation RTE** —
  `internal/pgnodes/readfuncs_query.go:324-325`: `if r.Rtekind != 0 { return
  nil, fmt.Errorf("pgnodes: unsupported RangeTblEntry.rtekind=%d (only
  RTE_RELATION)", r.Rtekind) }`.
- **The resolver rejects the SQL shapes `system_views.sql` is made of.**
  `resolver_query.go:109-121` requires exactly one `FROM` item with no joins, no
  subquery, no `TableFunc`, no alias and no column list;
  `rejectUnsupportedClauses` (`:195-203`) rejects CTEs, `DISTINCT`,
  `DISTINCT ON`, `GROUP BY`, `HAVING`, `ORDER BY`, `LIMIT`, `OFFSET`, set-ops,
  `VALUES`, window clauses, grouping sets and locking clauses. A bare `*` target
  is rejected too — the shape of twelve of the fourteen view-on-view definitions.
- **The measurement.** `pg_stat_replication`'s real `ev_action` contains
  (verified by grep over `internal/initdb/pg_stat_replication_ev_action.dat`)
  `:rtekind 3` ×2 (RTE_FUNCTION), `:rtekind 2` ×2 (RTE_JOIN), `:rtekind 0` ×1.
  Four of its five RTEs are kinds the reader refuses and the IR has no nodes for.

Closing that gap means reimplementing PG's parse analyzer — function-RTE
expansion with `funccoltypes`, join-tree construction with `joinaliasvars`,
`*`-expansion against a hosted relcache. **Capture is the mechanism**, it is
already the established one for all six existing blobs, and S7 makes it
mechanical.

### Sub-slice order (cheapest first)

`system_views.sql` defines **80** views. Classification below is from reading
every definition's `FROM`/`JOIN` targets, not from the plan's guesses.

**S9.1 — SRF-only, no catalog relation, no view dependency (23 new).** Their
`ev_action` should contain zero `:relid` and need zero OID mapping — the
`pg_stat_wal_receiver` / `pg_stat_recovery_prefetch` shape, i.e. the two
already-captured blobs with no `:relid` at all. `pg_locks` (`:397`),
`pg_cursors` (`:400`), `pg_prepared_statements` (`:424`), **`pg_settings`**
(`:604`), `pg_file_settings` (`:618`), `pg_hba_file_rules` (`:624`),
`pg_ident_file_mappings` (`:630`), `pg_timezone_abbrevs` (`:636`),
`pg_timezone_names` (`:644`), `pg_config` (`:647`), `pg_shmem_allocations`
(`:653`), `pg_shmem_allocations_numa` (`:661`), `pg_backend_memory_contexts`
(`:669`), `pg_stat_slru` (`:932`), `pg_stat_ssl` (`:996`), `pg_stat_gssapi`
(`:1009`), `pg_stat_archiver` (`:1139`), `pg_stat_io` (`:1171`), `pg_stat_wal`
(`:1195`), `pg_stat_progress_basebackup` (`:1309`),
`pg_replication_origin_status` (`:1370`), `pg_wait_events` (`:1401`), `pg_aios`
(`:1404`).

**Two corrections to the plan's S9.1 list.** `pg_settings` is SRF-only
(`FROM pg_show_all_settings()`), not a catalog view — it belongs here, not in
S9.2. And **`pg_stat_activity` is *not* SRF-only**: `system_views.sql:878` joins
`pg_stat_get_activity(NULL)` against `pg_authid` **and** `pg_database`, so it
carries `:relid 1260` and `:relid 1262` and belongs in S9.2.

*Sub-shape worth splitting out:* `pg_stat_bgwriter` (`:1150`) and
`pg_stat_checkpointer` (`:1157`) have **no `FROM` clause at all** — a target
list of bare zero-argument function calls, so their `Query` carries an
`RTE_RESULT`, a fifth RTE kind. Capture them last in S9.1 so an unexpected node
tag surfaces against a two-view blast radius.

*Precondition, in better shape than the plan assumes.*
`internal/initdb/pg_proc_seed_data.go` holds **3397** entries — exactly
upstream's `pg_proc.dat` count (`grep -c "^{ oid =>"` = 3397), generated by
`cmd/gen-pg-proc-data/main.go`. Every SRF sampled for S9.1 (`pg_lock_status`,
`pg_cursor`, `pg_prepared_statement`, `pg_show_all_settings`,
`pg_show_all_file_settings`, `pg_hba_file_rules`, `pg_ident_file_mappings`,
`pg_timezone_names`, `pg_config`, `pg_get_shmem_allocations_numa`,
`pg_get_backend_memory_contexts`, `pg_stat_get_slru`, `pg_stat_get_io`,
`pg_stat_get_wal`, `pg_stat_get_archiver`, `pg_get_wait_events`, `pg_get_aios`,
`pg_show_replication_origin_status`, `pg_stat_get_bgwriter_buf_written_clean`,
`pg_stat_get_checkpointer_num_timed`) is present, and each carries
`AllArgTypes` — the `proallargtypes`/`proargmodes`/`proargnames` OUT-arg
metadata (`pgProcEntry`, `internal/initdb/initdb.go:2606-2613`), which only 138
of the 3397 entries have. Keep the "pg_proc OID exists" gate as a per-view
*assertion*, not budgeted work.

**S9.2 — views over real catalogs.** `pg_roles` (`:17`), `pg_shadow` (`:35`),
`pg_group` (`:52`), `pg_policies` (`:73`), `pg_rules` (`:108`), `pg_views`
(`:118`), `pg_tables` (`:127`), `pg_matviews` (`:141`), `pg_indexes` (`:154`),
`pg_sequences` (`:167`), `pg_stats` (`:185`), `pg_stats_ext` (`:273`),
`pg_stats_ext_exprs` (`:307`), `pg_publication_tables` (`:381`),
`pg_available_extensions` (`:403`), `pg_available_extension_versions` (`:409`),
`pg_prepared_xacts` (`:417`), `pg_seclabels` (`:427`), the six `*_all_*` stat
bases (`:679`, `:718`, `:759`, `:803`, `:830`, `:856`), `pg_stat_activity`
(`:878`), `pg_stat_database` (`:1061`), `pg_stat_database_conflicts` (`:1103`),
`pg_stat_user_functions` (`:1115`), `pg_stat_xact_user_functions` (`:1127`), the
five `pg_stat_progress_*` views over `pg_database` (`:1204`, `:1226`, `:1247`,
`:1274`, `:1326`), `pg_user_mappings` (`:1346`), `pg_stat_subscription_stats`
(`:1384`).

These reference only **pinned** catalog OIDs, so still no view-OID mapping — but
they do exercise `RTE_JOIN` and, for `pg_stats_ext` / `pg_statio_all_tables`,
`LATERAL`. Verified: **`pg_views` does not exist in goopg in any form** (zero
non-test occurrences), while `pg_tables` (`catalog.go:8554`), `pg_settings`
(`:8574`) and `pg_roles` (`:8289`) exist as virtual tables — so for most of
S9.2 the on-disk row is a *second* definition of something goopg already
answers, and ceiling #3 applies.

**S9.3 — view-on-view chains (14 edges),** enumerated and evidenced in
`docs/design/0131-0008-system-view-oid-policy.md`. Every dependent carries the
base view's OID inside its `ev_action`; this is where S8's pinning decision pays
for itself. Cannot start before its base view from S9.2 is on disk.

**S9.4 — `information_schema` (65 views): expected to be deferred with a ledger
row,** not attempted in M0131. What it would need first, verified:

- **The namespace.** `pgNamespaceInitialEntries` (`internal/initdb/initdb.go:2328-2334`)
  bootstraps exactly three rows into `base/{1,5}/2615`: `pg_catalog` (11),
  `pg_toast` (99), `public` (2200). There is no `information_schema` namespace
  on disk, so a hosted PG cannot resolve a schema-qualified name at all.
- **The domains** — `sql_identifier`, `cardinal_number`, `character_data`,
  `yes_or_no`, `time_stamp`, every `information_schema` column's type. Zero
  occurrences of any of those names in non-test Go source, and the `pg_type`
  bootstrap seeds base types plus array peers only
  (`internal/initdb/pg_type_seed_data.go`, 193 entries).
- **The helper functions** (`_pg_expandarray`, `_pg_char_max_length`,
  `_pg_truetypid`, …) — SQL-language, in the `information_schema` namespace, a
  different `pg_proc` shape from anything currently seeded.

`postgres/src/backend/catalog/information_schema.sql` contains **65**
`CREATE VIEW` statements; each prerequisite above is a slice of its own. Treat
S9.4 as a successor milestone and file the ledger row when S9.3 lands.

## Known ceilings and unknowns (all ledgered, none assumed away)

1. **TOAST ceiling.** The largest committed blob is
   `internal/initdb/pg_stat_replication_ev_action.dat` at **27670 B** raw
   (`wc -c`; the others are 15601, 11116, 5928, 5397, 4158). The plan measures
   it compressing to 4235 B as a pglz varlena. `pglzVarlenaDatum`
   (`internal/initdb/pglz.go:36-47`) compresses anything at or over
   `pglzToastThreshold = 2048` (`:23-25`), falling back to a plain varlena when
   compression does not help. The bound is `MaxHeapTupleSize = BLCKSZ -
   MAXALIGN(SizeOfPageHeaderData + sizeof(ItemIdData))`
   (`postgres/src/include/access/htup_details.h:615`) ≈ **8160 B** at the default
   8 KiB block. The moment a capture's *compressed* size pushes a `pg_rewrite`
   tuple past that, out-of-line TOAST becomes required — the relation pair is
   declared upstream as `DECLARE_TOAST(pg_rewrite, 2838, 2839)`
   (`postgres/src/include/catalog/pg_rewrite.h:54`) and goopg bootstraps
   neither. **A separate, unscoped slice**, triggered by a specific capture
   overflowing. Compression ratio on `nodeToString` output is not predictable
   from raw size, so the trigger cannot be pre-computed: the tool must assert
   the bound and fail.
2. **`pg_proc` signature drift.** A captured blob pins `funcid`,
   `funcresulttype` and `funccoltypes` inside its `RangeTblFunction`/`FuncExpr`
   nodes. If goopg's `pg_proc` row for that OID disagrees on `prorettype` or
   OUT-arg types, a hosted PG builds a `TupleDesc` from one source and reads
   tuples shaped by the other. **Per-view check, not a blanket assumption** —
   the seed's upstream provenance makes agreement likely, but S9.1 alone lists
   23 views and this must be asserted 23 times.
3. **Dual definitions, and goopg's own routing between them is untested.**
   `pg_stat_replication` exists **twice**: as a virtual table registered by
   `registerStatReplicationView` (`internal/initdb/replication_views.go:34`,
   `Virtual: true` at `:69`) and as an on-disk nailed rel
   (`relcache_init.go:693` + `pgStatReplicationViewAttrs`, `:2593-2620`). They
   disagree on more than the plan states: the virtual definition has **24
   columns, every one typed `text`** — the 20 upstream columns plus `slot_name`,
   `send_buffer_hits`, `send_buffer_misses`, `send_buffer_bytes_resident` —
   while the on-disk definition has **20** columns with PG-faithful types
   (`int4`, `oid`, `name`, `inet` 869, `timestamptz` 1184, `xid` 28, `pg_lsn`
   3220, `interval` 1186). Column **count** and **types** both diverge. The
   pattern is proven for exactly one view, has never been stress-tested at
   scale, and goopg's planner resolution between the virtual entry and the heap
   `pg_class` row is covered by no gate — hence DS05 below.
4. **`reltype` is `2249` for every hosted view.** All six carry
   `RelType: 2249` (RECORDOID) where upstream creates a per-view composite
   `pg_type` row (`relcache_init.go:693-697`, rationale `:679-682`). M0131-S6.5
   probes whether plain `SELECT` after rule expansion touches it; if it does,
   every S9 view needs a composite `pg_type` row — an unscoped multiplier.
   **Unknown until S6.5 reports.**
5. **`RTE_RESULT` and `LATERAL` are unmeasured shapes.** No existing blob
   contains either (`rtekind` census over the six: `0`, `2`, `3` only).
   `pg_stat_bgwriter`/`pg_stat_checkpointer` and
   `pg_statio_all_tables`/`pg_stats_ext` are the first instances.
   Capture-and-see.

## Guards

1. **Per sub-slice: the E2E row-level view assertion, extended.** The gate from
   M0131-S5/S6 — `SELECT count(*) FROM <view>` on the hosted PG returning
   without 42809 — extended to each newly captured view. For S9.1 an SRF-backed
   view legitimately returns 0 rows on an idle cluster; assert *no error and the
   expected column set*, not a row count.
2. **The 42P01→resolvable transition is the acceptance criterion.** Before each
   capture, the hosted PG must fail `SELECT * FROM pg_catalog.<view>` with
   **42P01**; after, it must not. A test that only checks "no error afterwards"
   cannot distinguish a real fix from a view that was already resolvable.
3. **S7's oracle test stays green for the whole corpus**, not just the six:
   every `.dat` byte-reproducible from a fresh PG 18.3 `initdb`
   (`docs/design/0131-0007-ev-action-capture-tooling.md` guard #1).
4. **S8's OID invariant over the widened corpus**: no `:relid` in 12000..16383
   that is not a pinned goopg view OID
   (`docs/design/0131-0008-system-view-oid-policy.md` guard #3).
5. **Heap-tuple size assertion at capture time** — fail rather than silently
   overflow `MaxHeapTupleSize` (ceiling #1); the failure must name
   `DECLARE_TOAST(pg_rewrite, 2838, 2839)` as the next slice.
6. **Per-view `pg_proc` agreement check** (ceiling #2): every `funcid` in a
   captured blob resolves in `pgProcAllEntries()`, and its `RetType` /
   `AllArgTypes` match the blob's `funcresulttype` / `funccoltypes`.
7. **DS05 (TPC-DS SF0.5) on every sub-slice**, because the virtual/on-disk
   duality (ceiling #3) touches goopg's own relation resolution, not only the
   hosted PG's.
8. **S9.4 ledger row filed**, naming the namespace, the domains and the helper
   functions as its prerequisites — not left implicit.
9. UNITS + SMOKE green.

## References

- M0131 implementation plan §S9, `docs/design/0131-bidirectional-cluster-dir-coldstart-and-system-views.md`
- `docs/design/0131-0007-ev-action-capture-tooling.md`,
  `docs/design/0131-0008-system-view-oid-policy.md`,
  `docs/design/0131-0005-pg-rewrite-runtime-index-maintenance.md`,
  `docs/design/0131-0006-system-view-relhasrules-flip.md`
- `internal/catalog/catalog.go:335-342`, `:7026-7034`, `:7980`, `:11603`;
  `internal/executor/operators.go:61-63`; `internal/initdb/open.go:1885-2260`
- `internal/pgnodes/readfuncs_query.go:324-325`,
  `internal/pgnodes/resolver_query.go:109-121`, `:195-203`
- `postgres/src/backend/catalog/system_views.sql`,
  `postgres/src/backend/catalog/information_schema.sql`,
  `postgres/src/include/access/htup_details.h:615`,
  `postgres/src/include/catalog/pg_rewrite.h:54`
