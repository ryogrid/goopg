# System-view corpus widening — take the six-view island to `system_views.sql` scale

**Status:** in progress (S9.0, S9.1 landed)
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
tag surfaces against a two-view blast radius. (S9.1b did exactly that, and the
two-view blast radius earned its keep: the `RTE_RESULT` prediction was **wrong**
— an empty `:rtable` is what a FROM-less view actually serialises. See F4.)

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
   Capture-and-see. **RESOLVED, half wrongly — see F4 (S9.1b):** the
   FROM-less pair carries NO `RTE_RESULT` at all (it is a planner construct,
   absent from a parse tree); their blobs have an EMPTY `:rtable`, which is the
   shape actually gained. The `LATERAL` half stands unmeasured.

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

## S9.0 — the mechanical precondition (landed 2026-08-11)

S7.4 generated the views' `pg_class`/`pg_attribute` tables but left three
artefacts hand-edited, and ledgered them as S9.1's precondition: **the
`pg_rewrite` seed rows, the per-view `//go:embed` line for each `ev_action`
blob, and the view-OID constants.** Two of the three are now closed, which is
what makes S9.1's 23 views a capture-and-regenerate pass rather than 23×3 hand
edits.

**The `pg_rewrite` rows are generated.** `cmd/gen-nailed-view-tables` gained a
second emitter, `nailedViewRewriteEntries()`, rendered from the manifest's
`rule_oid` column. Every other field of an ON-SELECT rule is a constant of the
form — `_RETURN`, `ev_type` `'1'` (CMD_SELECT), `ev_enabled` `'O'` (ALWAYS),
`is_instead` true, `ev_qual` `"<>"` — so the generator writes them as literals
and derives nothing new. `pgRewriteInitialEntries()` is now a one-line delegate
and `replicationViewRewriteEntries()` (the five hand-written batched-29 rows) is
deleted. The invariant this buys: a view cannot reach disk seeded into
`pg_class` but absent from `pg_rewrite`, which is the
`cache lookup failed for rule …` FATAL that M0106-0010 Step 3dm phase B exists
to prevent and the most likely way a widening pass regresses.

**The `//go:embed` lines are gone**, replaced by one glob into an `embed.FS`
(`internal/initdb/nailed_view_ev_action.go`) resolved by view name.
`nailedViewEvAction(view)` panics — deliberately — on a missing or
non-parenthesised blob: the caller is bootstrap seed construction, and seeding
`ev_action` (BKI_FORCE_NOT_NULL) empty produces a cluster whose failure surfaces
much later inside a hosted PG's `stringToNode`, against a catalog nobody
suspects.

*The glob costs a guard the hand-written form got for free.* Six `//go:embed`
lines fail the **build** when a `.dat` is missing; a glob matching five files
instead of six compiles happily. So set equality is asserted explicitly
(`TestNailedViewEvActionBlobSetMatchesSeededViews`): every seeded view owns a
blob, every blob belongs to a seeded view — the second direction catching a
stale `.dat` for a view no longer in the manifest.
`TestNailedViewRewriteEntriesCoverEverySeededView` pins the `pg_rewrite` half
(one rule per view, no duplicate `ev_class`, the fixed rule form, and the
`ev_action` being *that view's own* blob), and
`TestNailedViewEvActionRejectsUnknownView` pins the loud-failure contract.

**The third artefact, the view-OID constants, is deliberately left.** They are
now referenced only by tests (`system_view_oid_pins_test.go`,
`pg_rewrite_bootstrap_test.go`) — the seed path reads OIDs from the manifest —
so they function as an independent hand-written pin that the generated table is
checked against. Generating them too would make both sides of that comparison
derive from one source. S9.1 adds no new constants.

Two order assumptions broke and were fixed rather than preserved, both because
generated rows follow the manifest's capture order:
`TestPgRewriteInitialEntriesContainsPgStatWalReceiverReturn` indexed
`entries[0]` and `TestPgRewriteRowLayout` (`btree_search_test.go`) read heap
**slot 1**. Both now locate the row by OID/name. Heap row order is not a
catalog invariant — PG reaches these rows by index or seqscan — and it will
shift again with every S9 capture, so a test that pins it is a test that fails
for the wrong reason. `TestPgRewriteRowLayout`'s `rawSize` expectation is now
`len(nailedViewEvAction("pg_stat_wal_receiver"))` rather than the literal 5928,
so a legitimate re-capture does not need the constant edited while a truncated
payload still fails.

Gates: `internal/initdb` PASS (61 s), `^TestE2E_` PASS (105 s, includes
`TestE2E_PGColdStartOnGoopgDataDir` — a real PG reading the reordered
`pg_rewrite` heap), UNITS PASS, pgbench smoke via the commit hook.

## Implementation status — S9.1 (2026-08-11)

**The on-disk corpus is 28 views, up from 6, and a real PG 18.3 hosted on a
goopg `$PGDATA` evaluates every one of them.** 22 of the 23 SRF-only views in
§"S9.1" landed; the tranche cost one capture run and one generator run, which
is the claim S9.0 was built to make true.

What the loop actually did:

1. **Pinned 22 new views** in `internal/initdb/system_view_oid_pins.go` (the
   hand-written policy table, now 28 rows in upstream-OID order). Checked
   against the oracle before pinning: every one of the 25 candidate views has
   **zero in-band `:relid`** in its `ev_action`, so the whole tranche could be
   pinned in one pass without ordering it by view-on-view edges.
2. **One capture, one regeneration.** `scripts/capture-ev-action.sh <28 views>`
   then `go run cmd/gen-nailed-view-tables/main.go >
   internal/initdb/nailed_view_seed_data.go`. No hand-edit of `nailedRel`,
   `nailedAttr`, the `pg_rewrite` seed rows, or any `//go:embed` line — S9.0's
   two closed hand-edits held at 4.7× the corpus size.
3. **Guard #5 (the `MaxHeapTupleSize` assertion) is implemented**, at capture
   time as the design asks: the script reads `pg_column_size(ev_action)` — the
   compressed size the oracle itself stores, which is the same pglz `goopg`'s
   `pglzVarlenaDatum` produces — and fails against an 8000 B budget
   (`MaxHeapTupleSize` 8160 minus 160 B of tuple header + the other seven
   columns), naming `DECLARE_TOAST(pg_rewrite, 2838, 2839)` as the prerequisite
   slice. Ceiling #1 measured, not assumed: the largest blob in the tranche
   stores at **1875 B**, the largest raw text is `pg_timezone_abbrevs` at
   9058 B — i.e. the raw-vs-stored distinction is what keeps the corpus inline,
   and a raw-size guard would have been wrong.
4. **Guards #1/#3/#4 extended to the whole corpus.**
   `assertNailedSystemViewsAreEvaluable` and
   `assertSystemViewOIDsArePinnedToUpstream` now share one literal probe set
   (`nailedSystemViewProbeSet`, `internal/testport/e2e_pg_coldstart_on_goopgdata_test.go`)
   covering all 28; `--verify` re-derives all 28 blobs **byte-identically**.

### Findings

**F1 — `pg_timezone_abbrevs` is blocked by `pg_amop`, not by capture.** It is
the only blob in the tranche carrying a `SortGroupClause`, and a hosted PG
rejects it with `operator 664 is not a valid ordering operator`:
`get_ordering_op_properties` looks (664 = `text_lt`, btree, strategy 1) up
through `pg_amop_fam_strat_index` (**2653**). The heap is not the problem —
`bootstrapPgAmopTuples` seeds all 945 `pg_amop.dat` rows — but 2653 is a bare
`makeBtreeRootPage()` placeholder in the three empty-root-page lists
(`initdb.go:1886/:2031/:2128`), so the index-only lookup finds nothing. **That
is M0131-S12's exact shape** (empty `pg_opclass_am_name_nsp_index` 2686,
`indexOK = true`, no seq-scan fallback) one catalog over, which makes it a
known-class blocker rather than a new one. The per-view
`t.Errorf` design (S6.6's risk control) is exactly what localised this — 27
views passed in the same run. Dropped from the tranche, pin removed, ledgered.
This generalises: **any S9 view with an `ORDER BY` in its definition needs
`pg_amop` bootstrapped first**, which was not on the ceiling list.

**F2 — three ceilings retired, one confirmed.** Ceiling #1 (inline tuple size)
is now measured and guarded. The "pg_proc signature drift" ceiling never fired:
every `atttypid` in the tranche resolved. The dual-definition hazard (ceiling
#3) did **not** bite at scale — a goopg server was probed directly and
`pg_settings`(42 rows), `pg_locks`, `pg_cursors`, `pg_prepared_statements`,
`pg_stat_wal`, `pg_config`(23), `pg_stat_io`(79) and `pg_timezone_names`(32)
all still answer from their VIRTUAL definitions with the heap `pg_class` rows
present. Confirmed instead: `pgTypeCanonical` (not `pg_type_seed_data.go`) is
the type set that matters, and it was missing `_int4`(1007), `numeric`(1700)
and `_regtype`(2211) — added. The capture script's guard #4 had been parsing
the *wider* seed file, making it vacuous for exactly this failure; it now parses
`pgTypeCanonical`'s case labels.

**F3 — the multi-page `pg_rewrite` heap is real now.** 28 seeded rules no
longer fit one 8 KB page, so `bootstrapPgRewriteTuples` returns TIDs with
`Block > 0` and the 2692/2693 btree leaves stamp them. A test that pinned
`wal_receiver` to page 0 failed and was rewritten to assert the invariant that
holds (every TID a real 1-based ItemPointer). A hosted PG reads the multi-page
heap through both indexes without complaint.

**Not done, ledgered:** `pg_stat_bgwriter`/`pg_stat_checkpointer` (the
`RTE_RESULT` pair, deliberately held to a two-view blast radius),
`pg_timezone_abbrevs` (F1), and guard #2's *before* half — the E2E asserts the
post-capture state but nothing mechanises the 42P01→resolvable transition.

Gates: `internal/initdb` PASS (64 s), `TestE2E_PGColdStartOnGoopgDataDir` PASS,
whole `^TestE2E_` family PASS (100 s), `capture-ev-action.sh --verify` PASS
(28/28 byte-identical), UNITS PASS, pgbench smoke via the commit hook.

## Implementation status — S9.1b (2026-08-11)

**The corpus is 30 views. The `RTE_RESULT` pair is in, and guard #2 is now a
whole guard.** Landed exactly as S9.0 promised — two rows added to
`systemViewOIDPins()`, one `scripts/capture-ev-action.sh <30 views>` run, one
`go run cmd/gen-nailed-view-tables/main.go`, plus the test-side probe entries.
No hand-edited blob, table or rule row.

The two pins were predicted from the band's own arithmetic before the oracle
was asked (`view`, `reltype = view+2`, `rule = view+3`, next view `= view+4`)
and the capture script's identity guard confirmed all four numbers for both
views — 12293/12295/12296 and 12297/12299/12300 — which is one more
independent datum for the "PG 18.3 initdb assignment is deterministic" claim
this policy rests on.

### Findings — S9.1b

**F4 — the `RTE_RESULT` premise was wrong, and the real shape is stronger.**
This slice existed because §"Two unmeasured `ev_action` shapes" predicted these
two views would carry an `RTE_RESULT` range-table entry. They do not. The
captured blob opens

```
({QUERY :commandType 1 … :cteList <> :rtable <> :rteperminfos <>
  :jointree {FROMEXPR :fromlist <> :quals <>} …
```

— the range table is **empty**. `RTE_RESULT` is a *planner* construct
(`postgres/src/backend/optimizer/plan/planmain.c`, from an empty jointree), not
a parse-tree one, so it never reaches `pg_rewrite.ev_action`. What the corpus
actually gained is the zero-RTE Query: every other blob has at least one
`:rtable` element, and this is the first proof that a hosted PG round-trips a
Query whose entire `rtable`/`fromlist` is `<>`. Both views evaluate on a hosted
PG. The prediction in §"Two unmeasured shapes" is corrected here rather than in
place; its `LATERAL` half (`pg_statio_all_tables`, `pg_stats_ext`) is untouched
and remains unmeasured.

**F5 — guard #2's before-half is mechanised, and it is a fail-when-fixed
lock.** `assertNonCorpusSystemViewIsStillAbsent`
(`internal/testport/e2e_pg_coldstart_on_goopgdata_test.go`) probes
`pg_catalog.pg_tables` — deliberately the named head of the S9.2 tranche — and
requires `42P01` *undefined_table*, not merely "an error": a 42809 or a
tupledesc `elog` would mean the row IS on disk and something downstream
rejected it, which is a different and interesting failure. With both halves in
place the evaluability probe now attributes its own cause: views in
`nailedSystemViewProbeSet()` evaluate, a view outside it does not exist at all.
When S9.2 lands this assertion fails and must be re-pointed at the next
un-adopted view.

**F6 — the dual-definition hazard bites `pg_stat_checkpointer`, by name.**
S9.1 found the hazard inert at scale; it is not inert here. goopg's runtime
virtual definition (`registerStatCheckpointerView`,
`internal/initdb/open.go:2625-2646`) has 11 columns — the same *count* as
upstream — but a different *set*: it omits `num_done` and carries an extra
`total_time`, and every column is declared `text` where upstream is
`int8`/`float8`/`timestamptz`. A count-only check would have passed. This is
ledgered, not fixed: the on-disk row is PG-faithful (it is captured), and
reconciling goopg's own virtual view is a separate slice. `pg_stat_bgwriter`'s
virtual definition agrees on all four names, order and types.

Gates: `internal/initdb` PASS (64 s), whole `^TestE2E_` family PASS (100 s),
`capture-ev-action.sh --verify` PASS (30/30 byte-identical), UNITS PASS,
pgbench smoke via the commit hook.

## Implementation status — S9.2a (2026-08-12)

**The corpus is 33 views and the first non-function-only tranche is on disk.**
`pg_views` (12028/12031), `pg_tables` (12033/12036) and `pg_matviews`
(12038/12041) were pinned in `internal/initdb/system_view_oid_pins.go`,
captured in ONE `scripts/capture-ev-action.sh <33 views>` run and rendered by
ONE `go run cmd/gen-nailed-view-tables/main.go > nailed_view_seed_data.go` —
again with no hand-edit of `nailedRel`/`nailedAttr`, the `pg_rewrite` seed rows
or any embed line, which is S9.0's claim holding across a shape change and not
merely across a bigger count.

**What is new about the shape.** Every previous blob was `FROM <SRF>` or had no
`FROM` at all. These three are joins: `pg_class` (1259) `LEFT JOIN`
`pg_namespace` (2615), plus `LEFT JOIN pg_tablespace` (1213) for `pg_tables`
and `pg_matviews`, so the corpus now exercises `RTE_JOIN` — and a hosted PG
18.3 evaluates all three (`assertNailedSystemViewsAreEvaluable`). The base
relations are sub-12000 bootstrap constants, so the blobs still carry zero
in-band `:relid` and the tranche needed no view-on-view ordering; S9.3 remains
the first slice that does.

### Findings — S9.2a

**F7 — TOAST ceiling #1 is no longer hypothetical: `pg_indexes` breaches it.**
`pg_indexes` was the tranche's fourth candidate and is the first view in the
corpus whose `ev_action` does not fit an inline heap tuple —
`pg_column_size` **9002 B** against guard #5's 8000 B budget (raw text 70408 B,
the largest in the corpus by 3.2×). Guard #5 is therefore a guard that has now
*fired* in anger rather than one that merely exists, and the ceiling's stated
prerequisite — `DECLARE_TOAST(pg_rewrite, 2838, 2839)` — is promoted from a
contingency to a named blocker for `pg_indexes` and for any later view of its
size. It is ledgered, and it is the new subject of guard #2 (below), which
means the prerequisite landing will announce itself as a test failure.

**Guard #2 re-pointed as its predecessor instructed.**
`assertNonCorpusSystemViewIsStillAbsent` probed `pg_tables` precisely because
`pg_tables` was S9.2's named head; adopting it flipped that assertion red, and
the helper now probes `pg_indexes`. That is a strict improvement in the guard's
quality: the previous subject was un-adopted only because nobody had got to it
yet, whereas the new one is un-adopted for a **measured** reason, so the
fail-when-fixed signal now names a specific prerequisite instead of a queue
position.

**F8 — the on-disk corpus is now AHEAD of goopg's own virtual corpus.** A
goopg server was probed directly, as S9.1 did. `pg_tables` still answers from
its virtual definition (2 rows) and `pg_indexes` answers 0, so seeding on-disk
rows did not break goopg's own client-visible answers — ceiling #3 did not bite
in the regression direction. But `pg_views` and `pg_matviews` fail **42P01 on
goopg** while a PG hosted on goopg's directory evaluates them, i.e. the two
engines now disagree about which system views exist on the same cluster in the
*opposite* direction from the one this milestone started with. Ledgered.

**F9 — `pg_tables` is the widest dual definition measured so far.** Sharper
than F6's count-preserving mismatch: goopg's virtual `pg_tables`
(`internal/catalog/catalog.go:8552-8566`) has **3** columns
(`schemaname`, `tablename`, `tableowner`), all `text`, under a synthetic OID
`1259101`, while the on-disk row upstream pins is **8** columns with
PG-faithful types under OID 12033. Count, type and OID all diverge. Ledgered,
not fixed — reconciling the virtual definitions is a slice of its own and it
changes goopg-client-visible output.

Gates: `internal/initdb` PASS (84 s), `TestE2E_PGColdStartOnGoopgDataDir` PASS,
whole `^TestE2E_` family PASS (90 s), `capture-ev-action.sh --verify` PASS
(33/33 byte-identical), `go vet` clean, UNITS PASS, pgbench smoke via the
commit hook.

## Implementation status — S9.2b (2026-08-12)

**The corpus is 35 views and it now reads SHARED catalogs.** `pg_roles`
(12000/12003) and `pg_stat_activity` (12226/12229) — S9.2's two remaining
named heads — were pinned in `internal/initdb/system_view_oid_pins.go`,
captured in ONE `scripts/capture-ev-action.sh <35 views>` run and rendered by
ONE `go run cmd/gen-nailed-view-tables/main.go > nailed_view_seed_data.go`.
The other 33 `.dat` files came back byte-identical from that same run, which is
a determinism re-measurement the tranche gets for free.

**What is new about the shape.** S9.2a's joins were over *local* catalogs
(`pg_class` 1259, `pg_namespace` 2615, `pg_tablespace` 1213 — the last is
shared but nailed). These two read the shared catalogs a hosted PG resolves
through `base/5`: `pg_roles` is `FROM pg_authid` (1260) `LEFT JOIN
pg_db_role_setting` (2964), and `pg_stat_activity` joins
`pg_stat_get_activity(NULL)` against `pg_authid` **and** `pg_database` (1262) —
the corpus's first blob mixing an SRF with catalog relations in one join tree,
which is the exact shape S9's filing predicted would need measuring. A hosted
PG 18.3 evaluates both (`assertNailedSystemViewsAreEvaluable`), so no new RTE
kind or shared-relation resolution gap fell out. Both base sets are sub-12000
bootstrap constants, so the blobs still carry zero in-band `:relid` (measured:
`pg_roles` embeds 1260/2964, `pg_stat_activity` 1260/1262) and S9.3 remains the
first slice needing view-on-view ordering.

**Guard #5 was checked BEFORE pinning, per S9.2a's own advice**, on a throwaway
PG 18.3 with `pg_column_size(r.ev_action)`: `pg_roles` 2167 B and
`pg_stat_activity` 4780 B against the 8000 B inline budget — both comfortable.
The same probe sized six further S9.2/S9.3 candidates and found a **second**
TOAST-ceiling breach beyond `pg_indexes` (9002 B): `pg_seclabels` at
**35379 B** stored (203378 B raw), 4.4× `pg_indexes` and by far the largest
blob measured. Ledgered — it widens the `DECLARE_TOAST(pg_rewrite, 2838, 2839)`
blocker from one view to a class, and 35 KB will not fit an inline tuple by any
amount of header shaving. Under the ceiling and still unadopted:
`pg_shadow` 2015 B, `pg_group` 1428 B, `pg_user` 1356 B, `pg_rules` 3774 B,
`pg_policies` 5439 B, `pg_stat_database` 2721 B, `pg_stat_all_tables` 5473 B.

Guard #2 (`assertNonCorpusSystemViewIsStillAbsent`) is UNCHANGED this slice:
its subject `pg_indexes` is still un-adopted for the measured TOAST reason, so
unlike S9.2a there was no red assertion to re-point.

### Findings — S9.2b

**F10 — the dual-definition hazard, third measurement, and this time goopg's
own answers are the ones that survive.** Probed on a live goopg server
(fresh `init`, port 5533): unlike `pg_views`/`pg_matviews` in F8, both of these
views DO answer on goopg — `pg_roles` returns 18 rows and `pg_stat_activity`
4 — so seeding did not disturb the virtual path and the two engines agree that
the views exist. They disagree about everything else:

| view | goopg virtual | on-disk / upstream |
|---|---|---|
| `pg_roles` | OID 1259102, **4** cols (`oid`, `rolname`, `rolsuper`, `rolcanlogin`) | OID 12000, **13** cols, PG-typed |
| `pg_stat_activity` | OID **16403**, **21** cols (no `query_id`) | OID 12226, **22** cols, PG-typed |

`pg_roles` at 4-vs-13 is the widest column-count gap measured in the milestone
(F9's `pg_tables` was 3-vs-8). The sharper finding is `pg_stat_activity`'s
**16403**: that is in the `FirstUserOID = 16384` band, i.e. a *system* view
whose virtual relation was minted by the runtime user-relation allocator, so
its OID is not even stable across clusters — a strictly worse divergence than
the synthetic `1259xxx` values F9 found, and one that would collide with a user
relation's OID on a cluster that created one first. Ledgered, not fixed:
reconciling the virtual definitions changes goopg-client-visible output and is
a slice of its own.

Gates: `internal/initdb` PASS (93 s), `TestE2E_PGColdStartOnGoopgDataDir` PASS,
whole `^TestE2E_` family PASS (90 s), `capture-ev-action.sh --verify` PASS
(35/35 byte-identical), `go build ./...` + `go vet` clean, UNITS PASS, pgbench
smoke via the commit hook.

## Implementation status — S9.2c (2026-08-12)

**The corpus crosses its first view-on-view edge, one slice earlier than S9.3.**
Pinned and seeded: `pg_shadow` (12005/12008) and `pg_user` (12014/12017),
taking the on-disk corpus from 35 to **37** views. `pg_shadow` is ordinary
S9.2 work — `FROM pg_authid` (1260) `LEFT JOIN pg_db_role_setting` (2964),
the same shared-catalog shape S9.2b proved. `pg_user` is not: it is
`FROM pg_shadow` (`system_views.sql:60-71`), and its captured blob carries
**`:relid 12005`** — measured, the **first in-band `:relid` in the entire
corpus**, after 35 views that carried none.

That single number is the acceptance test for the whole Option-A policy of
`0131-0008`. Because goopg pins its view OIDs to upstream's own initdb
assignments, the OID `pg_user`'s blob embeds is *already* the OID goopg's
`pg_class` heap gives `pg_shadow` — nothing inside the blob is rewritten, and
the ordering requirement (base pinned before dependent) is enforced
mechanically by capture guard #4 rather than trusted. The measurement is the
E2E probe: a hosted PG 18.3 evaluating `SELECT * FROM pg_user` must resolve
12005 through goopg's `pg_class`, find `pg_shadow`'s `_RETURN` row in goopg's
`pg_rewrite`, and substitute a second Query — the first probe in this lane
whose success needs TWO of goopg's rule rows and a relid lookup between them.
It passes. **S9.3's mechanism is therefore proven; what remains for S9.3 is
scale and ordering, not feasibility.**

### Findings — S9.2c

**F11 — ceiling #3, and it is a `pg_type` bootstrap gap.** `pg_group` (12010)
was captured and pinned alongside the other two — it is catalog-direct
(`pg_authid` + `pg_auth_members` 1261) and at **1428 B** stored it clears every
size ceiling — but a hosted PG refuses to evaluate it:

```
ERROR:  could not find array type for data type oid
STATEMENT:  SELECT * FROM pg_catalog.pg_group LIMIT 0
```

`pg_group`'s `grolist` column is `ARRAY(SELECT member FROM pg_auth_members …)`,
making it the corpus's first blob with an `ARRAY(SubLink)` target entry.
Evaluating it sends PG through `get_array_type`, which reads
`pg_type.typarray` for `OIDOID`. goopg seeds that column as a **literal 0 for
every row** (`internal/initdb/pg_type_bootstrap.go:306`), even though the
`_oid` row (1028) itself is present in `pg_type_seed_data.go` and the
`typarray` column exists in the tupledesc. So this is a *catalog* gap, not a
capture gap — the same class as `pg_timezone_abbrevs`' missing `pg_amop` row
(F5), and the third distinct ceiling the corpus has hit after the TOAST class
(F6/F7). `pg_group` is therefore NOT pinned; the resume point is populating
`typarray` (and `typelem`) from `pg_type.dat` across the seeded set. Ledgered.

Worth stating plainly: all three ceilings found so far — `pg_amop`, `pg_type
.typarray`, `pg_rewrite` TOAST — are gaps in what goopg's initdb *bootstraps*,
not in the capture mechanism. The capture tooling has not been the limit once.

Gates: `internal/initdb` PASS (87 s), `TestE2E_PGColdStartOnGoopgDataDir` PASS,
whole `^TestE2E_` family PASS (92 s), `capture-ev-action.sh --verify` PASS
(37/37 byte-identical), `go build ./...` + `go vet` clean, UNITS PASS, pgbench
smoke via the commit hook.

## Implementation status — S9.2d (2026-08-12)

**S9.2's catalog-direct tranche is finished: the corpus is 43 views, and the
two views that did not make it named ceilings #4 and #5.** Pinned and seeded in
one capture run: `pg_rules` (12023/12026), `pg_sequences` (12048/12051),
`pg_prepared_xacts` (12090/12093), `pg_stat_database` (12270/12273),
`pg_stat_database_conflicts` (12275/12278) and `pg_user_mappings`
(12338/12341) — 37 → **43**. The other 37 blobs and the manifest came back
byte-identical, and `--verify` re-derives all 43 against a fresh throwaway
PG 18.3.

Selection was measured rather than guessed. A throwaway oracle was asked, for
every `pg_catalog` view, for its stored `ev_action` size *and* the set of
`:relid` values its blob carries inside the 12000..16383 band; the catalog-
direct set (no in-band relid) under the 8000 B inline budget is exactly the
population S9.2 owns. That query also fixes S9.3's remaining work as a list:
twelve views (`pg_stat_sys_tables`/`pg_stat_user_tables` on 12146,
`pg_stat_xact_*_tables` on 12151, `pg_statio_*_tables` on 12174,
`pg_stat_*_indexes` on 12187, `pg_statio_*_indexes` on 12200,
`pg_statio_*_sequences` on 12213), each depending on a single `pg_stat_all_*`
base that is itself catalog-direct — and five of those six bases are under the
inline ceiling, the exception being `pg_statio_all_tables` at 10475 B, which
puts its two dependents behind the `pg_rewrite` TOAST work (F6/F7).

**`pg_stat_database` is the corpus's first blob carrying a set operation.**
`system_views.sql:1006-1010` selects from
`(SELECT 0 AS oid, NULL::name AS datname UNION ALL SELECT oid, datname FROM
pg_database)`, so the Query has an `RTE_SUBQUERY` whose own Query is a
`SetOperationStmt` — a shape none of the previous 37 blobs exercised, and it
round-trips through the same verbatim-capture path with no special handling.
The two "unmeasured `ev_action` shapes" this doc listed (`RTE_RESULT`,
`LATERAL`) are now one: `RTE_RESULT` was closed by S9.1b, `LATERAL` remains.

### Findings — S9.2d

**F12 — ceiling #4: `pg_policy` (3256) is not an on-disk relation.** `pg_policies`
(12018, 5439 B stored, well under every size ceiling) captures cleanly and a
hosted PG then fails it with `could not open relation with OID 3256`. This is
a new *kind* of ceiling: F5/F11 were missing rows or columns inside catalogs
goopg does bootstrap, whereas here the blob's base **catalog itself** is
absent from a goopg cluster. It is the first captured view blocked that way,
and it says the remaining corpus is gated not only on catalog *content* but on
which system catalogs goopg materialises at all. Ledgered; the resume point is
bootstrapping `pg_policy` (and re-pinning `pg_policies`), not touching the
capture tool.

Incidentally, `pg_policies.roles` is the corpus's first `name[]` column, which
is why capture guard #5 demanded `_name` (1003) and it is now canonical in
`pg_type_bootstrap.go` — with `typalign` `'i'`, **not** `name`'s own `'c'`
(`Catalog.pm:469` gives an array `'d'` only when its element is `'d'`).

**F13 — ceiling #5: `pg_type.typelem` is a literal 0 for every row, the exact
twin of F11.** `pg_publication_tables` (12068, 3793 B) is rejected by a hosted
PG with:

```
ERROR:  target type is not an array
STATEMENT:  SELECT * FROM pg_catalog.pg_publication_tables LIMIT 0
```

That message is raised by `ExecInitExprRec`'s `T_ArrayCoerceExpr` arm
(`postgres/src/backend/executor/execExpr.c:1684-1688`) when
`get_element_type(resulttype)` returns InvalidOid — i.e. when
`pg_type.typelem` is 0 for the array type being coerced to. goopg's `pgTypeRow`
writes `typelem` as a hardcoded 0 (column 14) one line above the hardcoded
`typarray` 0 (column 15) that F11 found. So ceilings #3 and #5 are **the same
defect, one column apart**, reached from two different directions
(`get_array_type` for F11, `get_element_type` here), and one fix — populating
`typelem`/`typarray` from `pg_type.dat` — closes both and unblocks both
`pg_group` and `pg_publication_tables`. Ledgered.

The generalisation from S9.2c survives at five for five: **every ceiling this
milestone has hit is a gap in what goopg's initdb bootstraps** (`pg_amop` rows,
`pg_type.typarray`, `pg_rewrite` TOAST, the `pg_policy` catalog,
`pg_type.typelem`). The capture tooling has not been the limit once.

One type-table addition was needed for a view that DID land: `regtype` (2206)
is `pg_sequences.data_type`, the first nailed-view column of the scalar
OID-alias type — its array (2211) had been canonical since S9.1. Values taken
from `pg_type.dat:389-392` and `pg_proc.dat` (2220/2221, 2454/2455).

Gates: `internal/initdb` PASS (96 s), `TestE2E_PGColdStartOnGoopgDataDir` PASS,
whole `^TestE2E_` family PASS (91 s), `capture-ev-action.sh --verify` PASS
(43/43 byte-identical), `go build ./...` + `go vet` clean, UNITS PASS, pgbench
smoke via the commit hook.

## Implementation status — S9.3a (2026-08-12)

**S9.3 opens: the corpus is 49 views and now carries four view-on-view edges
over two bases, pinned in a single capture run.** The per-table statistics
family landed complete except for its I/O triple:

| view | OID / rule | stored `ev_action` | in-band `:relid` |
|---|---|---|---|
| `pg_stat_all_tables` | 12146 / 12149 | 5473 B | — |
| `pg_stat_xact_all_tables` | 12151 / 12154 | 5057 B | — |
| `pg_stat_sys_tables` | 12156 / 12159 | 2476 B | 12146 |
| `pg_stat_xact_sys_tables` | 12161 / 12164 | 1822 B | 12151 |
| `pg_stat_user_tables` | 12165 / 12168 | 2478 B | 12146 |
| `pg_stat_xact_user_tables` | 12170 / 12173 | 1824 B | 12151 |

Mechanics unchanged from S9.2: one `scripts/capture-ev-action.sh <49 views>`
run plus one `cmd/gen-nailed-view-tables` run; the other 43 blobs came back
byte-identical and `--verify` re-derives all 49 against a fresh throwaway
PG 18.3. No hand-edited blob, table, rule row or `//go:embed` line — S9.0's
claim now holds at 8.2× the original corpus.

What is new is that the **edges are the subject, not an incident.** S9.2c
crossed one edge (`pg_user` → `pg_shadow`); this tranche crosses four over two
distinct bases in one pass, and capture guard #4 (a dependent may not be pinned
before its base) is what orders the pin table rather than care. Each dependent
is `SELECT * FROM <base> WHERE …` (`system_views.sql`), so under Option-A
identity pinning its embedded 12146/12151 is already correct the moment the
base is pinned above it; a hosted PG evaluating all six in
`assertNailedSystemViewsAreEvaluable` is the acceptance measurement.

### Findings — S9.3a

**F14 — a dependent costs an order of magnitude less than its base, and that
is a property of the pin policy, not of the views.** The four dependents store
at 1822–2478 B against bases of 5057–5473 B, because a rewritten
`FROM pg_stat_all_tables WHERE …` Query stores the base as one `RTE_RELATION`
naming OID 12146 instead of re-expanding its 30-column SRF join tree. The
practical consequence for the rest of S9.3: **dependents are never the thing
that breaches the inline-tuple ceiling — bases are.** Ceiling #1 therefore
propagates *downward* through the dependency graph, which is exactly what
happened to the `pg_statio_*_tables` triple below, and it means the remaining
S9.3 cost is bounded by six base captures, not by twelve.

**Ceiling #1 claims its first *dependents*.** `pg_statio_all_tables` (12174)
stores at 10475 B, over the script's 8000 B inline budget, so it cannot be
seeded — and because guard #4 forbids pinning `pg_statio_sys_tables` (12179) or
`pg_statio_user_tables` (12183) ahead of their base, one over-ceiling base
withholds **three** views rather than one. This is the first time a ceiling has
propagated along an edge, and it re-prices the `pg_rewrite` TOAST
(2838/2839) work: it now gates `pg_indexes` + this triple. Ledgered.

The S9.2c/S9.2d generalisation still holds at five for five — every ceiling in
this milestone is a gap in what goopg's initdb bootstraps, never in the capture
tooling. S9.3a added no new ceiling *kind*; it added reach for an existing one.

Remaining in S9.3 after this slice: three bases (`pg_stat_all_indexes` 12187,
`pg_statio_all_indexes` 12200, `pg_statio_all_sequences` 12213, all under the
ceiling at 6826/6799/2431 B) with their six dependents, plus the
`pg_statio_*_tables` triple behind TOAST.

Gates: `internal/initdb` PASS (96 s), `TestE2E_PGColdStartOnGoopgDataDir` PASS,
whole `^TestE2E_` family PASS (92 s), `capture-ev-action.sh --verify` PASS
(49/49 byte-identical), `go build ./...` + `go vet` clean, UNITS PASS, pgbench
smoke via the commit hook.

## Implementation status — S9.3b (2026-08-12)

**S9.3's reachable population is finished: the corpus is 58 views over TEN
view-on-view edges and five bases.** The per-index and per-sequence statistics
families landed in one capture run:

| view | OID / rule | stored `ev_action` | in-band `:relid` |
|---|---|---|---|
| `pg_stat_all_indexes` | 12187 / 12190 | 6826 B | — |
| `pg_stat_sys_indexes` | 12192 / 12195 | 1714 B | 12187 |
| `pg_stat_user_indexes` | 12196 / 12199 | 1716 B | 12187 |
| `pg_statio_all_indexes` | 12200 / 12203 | 6799 B | — |
| `pg_statio_sys_indexes` | 12205 / 12208 | 1625 B | 12200 |
| `pg_statio_user_indexes` | 12209 / 12212 | 1628 B | 12200 |
| `pg_statio_all_sequences` | 12213 / 12216 | 2431 B | — |
| `pg_statio_sys_sequences` | 12218 / 12221 | 1559 B | 12213 |
| `pg_statio_user_sequences` | 12222 / 12225 | 1561 B | 12213 |

Mechanics unchanged and now boring, which is the point: one
`scripts/capture-ev-action.sh <58 views>` run plus one
`cmd/gen-nailed-view-tables` run; the other 49 blobs came back byte-identical
and `--verify` re-derives all 58 against a fresh throwaway PG 18.3. No
hand-edited blob, table, rule row or `//go:embed` line — S9.0's claim holds at
9.7× the original corpus, and S9.3a's four-edge exercise of guard #4 scales to
six more edges without a tooling change.

### Findings — S9.3b

**F15 — F14's dependent/base ratio is a structural constant, not a
coincidence of the table family.** Across all three new bases, each dependent
stores at 23–64 % of its base (1714/6826 = 25 %, 1625/6799 = 24 %,
1559/2431 = 64 %), and the ratio tracks *how much of the base's tree is SRF
join* rather than anything about the dependent: the two index bases expand a
9- and 7-column `pg_stat_get_*` join, while `pg_statio_all_sequences` is a
small 5-column tree whose dependents therefore cannot save as much. The
prediction from F14 — that only bases can breach the inline ceiling — now holds
at 5 bases / 10 dependents with no counterexample. The tightest margin measured
so far is `pg_stat_all_indexes` at 6826 B against the 8000 B budget: 85 % of
inline capacity, which is a standing argument that the `pg_rewrite` TOAST slice
is a near-term prerequisite for further base captures, not a distant one.

**No new ceiling.** Every view S9.3 can reach is now seeded; what remains of
S9.3 is exactly the `pg_statio_*_tables` triple withheld by ceiling #1 (base
`pg_statio_all_tables` 12174 at 10475 B), unchanged from S9.3a. The
"every ceiling is an initdb-bootstrap gap" generalisation holds at five for
five — S9.3b added no ceiling of any kind, and is the first slice in S9 that
found no new finding class at all.

Remaining in S9: the `pg_statio_*_tables` triple behind `pg_rewrite` TOAST
(2838/2839), `pg_indexes` behind the same, `pg_group` +
`pg_publication_tables` behind `pg_type.typelem`/`typarray` population (F13),
`pg_timezone_abbrevs`, and S9.4's 65 `information_schema` views (expected to be
deferred with a ledger row).

Gates: `internal/initdb` PASS (103 s), whole `^TestE2E_` family PASS (92 s,
includes `TestE2E_PGColdStartOnGoopgDataDir`), `capture-ev-action.sh --verify`
PASS (58/58 byte-identical), `go build ./...` + `go vet` clean, UNITS PASS,
pgbench smoke via the commit hook.

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
