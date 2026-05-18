# 07 — System Views and pg_rewrite Rules

**Status:** draft
**Date:** 2026-05-19
**Milestone:** M0106 (PG Relcache Init File Compatibility); replaces the
reactive `docs/design/0106-0010-step3d[i-m]-*.md` chain for
`pg_stat_wal_receiver` and the rest of the replication views.

---

## Scope

This file specifies the catalog state that goopg's `initdb` must
deposit to support PG18's **`system_views.sql`** phase output as a
vanilla PG18 backend would observe it on attach, with a deliberate
focus on the **six replication-related views** that the
goopg→PG failover E2E (`TestE2E_FailoverGoopgToPG/async`) actually
queries during recovery.

In scope:

- View entries in `pg_class` (relkind `'v'`, relhasrules=`true`) and
  the per-column `pg_attribute` rows.
- The `pg_rewrite._RETURN` rule tuple per view, including
  `ev_action` (the nodeToString-serialised `Query` parse tree).
- The `pg_proc` rows for the set-returning functions (SRFs) that
  back the replication / stats views — in particular
  `pg_stat_get_wal_receiver` (OID 3317), whose 15-column composite
  output type and OUT-arg arrays are load-bearing for the M0106
  failover path.
- The six replication views:
  - `pg_stat_replication`
  - `pg_stat_wal_receiver`  (**load-bearing for M0106**)
  - `pg_stat_subscription`
  - `pg_stat_recovery_prefetch`
  - `pg_replication_slots`
  - `pg_stat_replication_slots`
- Brief mention of the activity / stats views that client queries on
  the standby may incidentally touch
  (`pg_stat_activity`, `pg_stat_database`, `pg_stat_bgwriter`,
  `pg_stat_archiver`, `pg_stat_wal`). Full row layouts for those
  views are deferred.

Out of scope (covered elsewhere or deferred):

- The `pg_rewrite` catalog tuple header / TupleDesc / index layout
  on disk — see [`05-local-catalog-bootstrap.md`](05-local-catalog-bootstrap.md).
  This file consumes that layout to insert `_RETURN` rule tuples;
  it does not redefine it.
- The streaming-replication wire protocol and the runtime
  `WalRcv` shared-memory structure populated by the walreceiver
  process — see
  [`09-streaming-replication-readiness.md`](09-streaming-replication-readiness.md).
- The remaining `system_*.sql` phases of initdb
  (`information_schema.sql`, `snowball_create.sql`,
  `system_constraints.sql`, `system_functions.sql`,
  `system_privileges.sql`). These produce additional pg_proc /
  pg_class / pg_namespace rows that PG's SQL-time installer would
  emit; goopg should treat them as a single follow-up after the
  replication-view coverage in this file lands.

---

## Upstream references

Paths are relative to `postgres/` in the goopg tree; line numbers
match the vendored PG 18.3.

| Symbol | File:line |
|---|---|
| `system_views.sql` (replication block) | `src/backend/catalog/system_views.sql:906-1059` |
| `CREATE VIEW pg_stat_replication` | `src/backend/catalog/system_views.sql:906` |
| `CREATE VIEW pg_stat_wal_receiver` | `src/backend/catalog/system_views.sql:945` |
| `CREATE VIEW pg_stat_recovery_prefetch` | `src/backend/catalog/system_views.sql:965` |
| `CREATE VIEW pg_stat_subscription` | `src/backend/catalog/system_views.sql:979` |
| `CREATE VIEW pg_replication_slots` | `src/backend/catalog/system_views.sql:1019` |
| `CREATE VIEW pg_stat_replication_slots` | `src/backend/catalog/system_views.sql:1045` |
| `FormData_pg_rewrite` (8 cols) | `src/include/catalog/pg_rewrite.h:32-45` |
| `RewriteRelationId = 2618`, `RewriteRelRulenameIndexId = 2693` | `src/include/catalog/pg_rewrite.h:56-57` |
| `MAKE_SYSCACHE(RULERELNAME, …)` | `src/include/catalog/pg_rewrite.h:59` |
| `InsertRule()` | `src/backend/rewrite/rewriteDefine.c:52` |
| `DefineRule()` | `src/backend/rewrite/rewriteDefine.c:190` |
| `DefineQueryRewrite()` (rule body + `InsertRule` call) | `src/backend/rewrite/rewriteDefine.c:224, :470` |
| `RemoveRewriteRuleById()` | `src/backend/rewrite/rewriteRemove.c:33` |
| `RenameRewriteRule()` | `src/backend/rewrite/rewriteDefine.c:793` |
| `DefineView()` | `src/backend/commands/view.c:356` |
| `DefineViewRules()` | `src/backend/commands/view.c:332` |
| `RenameRelation()` (covers `ALTER VIEW … RENAME`) | `src/backend/commands/tablecmds.c:4206` |
| `RenameRelationInternal()` | `src/backend/commands/tablecmds.c:4270` |
| `AlterTableNamespace()` (covers `ALTER VIEW … SET SCHEMA`) | `src/backend/commands/tablecmds.c:18946` |
| `pg_stat_get_wal_receiver()` SRF body | `src/backend/replication/walreceiver.c:1392-1517` |
| `WalRcvData *WalRcv` (shared-memory pointer) | `src/backend/replication/walreceiverfuncs.c:34, :59` |
| `WalRcvData` struct layout | `src/include/replication/walreceiver.h:43-165` |
| `pg_stat_get_wal_senders()` | `src/backend/replication/walsender.c:3914` |
| `pg_get_replication_slots()` | `src/backend/replication/slotfuncs.c:236` |
| `ReplicationSlotCtl->replication_slots[]` | `src/backend/replication/slot.c:144, :211, :224` |
| `pg_stat_get_replication_slot()` | `src/backend/utils/adt/pgstatfuncs.c:2113` |
| `pg_stat_get_subscription()` | `src/backend/replication/logical/launcher.c:1301` |
| `pg_stat_get_recovery_prefetch()` | `src/backend/access/transam/xlogprefetcher.c:824` |
| `pg_proc.dat` row for 3317 (pg_stat_get_wal_receiver) | `src/include/catalog/pg_proc.dat:5668-5674` |
| `pg_proc.dat` row for 3099 (pg_stat_get_wal_senders) | `src/include/catalog/pg_proc.dat:5659-5667` |
| `pg_proc.dat` row for 6118 (pg_stat_get_subscription) | `src/include/catalog/pg_proc.dat:5695-5702` |
| `pg_proc.dat` row for 6169 (pg_stat_get_replication_slot) | `src/include/catalog/pg_proc.dat:5675-5681` |
| `pg_proc.dat` row for 6248 (pg_stat_get_recovery_prefetch) | `src/include/catalog/pg_proc.dat:6027-6033` |
| `pg_proc.dat` row for 3781 (pg_get_replication_slots) | `src/include/catalog/pg_proc.dat:11464-11472` |
| `FirstUnpinnedObjectId = 12000`, `FirstNormalObjectId = 16384` | `src/include/access/transam.h:196-197` |
| `nodeToString()` (serialises `Query` for `ev_action`) | `src/backend/nodes/outfuncs.c:805` |
| `CacheInvalidateRelcache()` | `src/backend/utils/cache/inval.c:1635` |
| `RelationCacheInvalidateEntry()` (SI consumer) | `src/backend/utils/cache/inval.c:856` |

> **OID correction.** The brief listed `pg_stat_get_recovery_prefetch`
> as OID 8400. The actual `pg_proc.dat` row is **OID 6248**
> (`src/include/catalog/pg_proc.dat:6027`). This file uses 6248 in
> all subsequent tables; 8400 is unused in vendored PG18.

---

## Initdb-time output

### View inventory

The six replication views all live in schema `pg_catalog`
(`pronamespace = 11`). goopg assigns view OIDs from the
`FirstUnpinnedObjectId..FirstNormalObjectId` range (12000..16383 —
`transam.h:196-197`) so they do not collide with upstream-pinned OIDs
even though the views' identities are *named* matches, not OID
matches. `pg_stat_wal_receiver` is already pinned to **OID 12100** by
Step 3dl (`internal/initdb/relcache_init.go:678`); the remaining five
view OIDs are reserved here in a contiguous block to make the
`pg_rewrite._RETURN` ev_class values trivially stable across loops.

The "Backing SRF OID" column is the `pg_proc.oid` of the function
that produces the view's underlying composite tuple. The "Result
type OID" is the function's `prorettype`; for `pg_stat_get_*` SRFs
that return a single composite (`RECORD`) row it is **2249**
(`pg_type.oid` for `record`).

| View | Goopg pg_class OID | Backing SRF | SRF OID | Cite |
|---|---:|---|---:|---|
| pg_stat_replication | 12102 (proposed) | pg_stat_get_wal_senders + pg_stat_get_activity | 3099 (+ 2022) | `system_views.sql:906-930` |
| pg_stat_wal_receiver | 12100 (Step 3dl) | pg_stat_get_wal_receiver | 3317 | `system_views.sql:945-963` |
| pg_stat_recovery_prefetch | 12103 (proposed) | pg_stat_get_recovery_prefetch | 6248 | `system_views.sql:965-977` |
| pg_stat_subscription | 12104 (proposed) | pg_stat_get_subscription + pg_subscription | 6118 | `system_views.sql:979-994` |
| pg_replication_slots | 12105 (proposed) | pg_get_replication_slots + pg_database | 3781 | `system_views.sql:1019-1043` |
| pg_stat_replication_slots | 12106 (proposed) | pg_stat_get_replication_slot, joined laterally to pg_replication_slots | 6169 | `system_views.sql:1045-1059` |

Stats views that a generic client query may incidentally hit, called
out for completeness; their full layouts are deferred to a follow-up
file:

| View | Backing SRF (primary) | Cite |
|---|---|---|
| pg_stat_activity | pg_stat_get_activity (OID 2022) | `system_views.sql:864-904` |
| pg_stat_database | pg_stat_get_db_* (OIDs 1934, 1936, …) | `system_views.sql:1061+` |
| pg_stat_bgwriter | pg_stat_get_bgwriter_* | (follow-up) |
| pg_stat_archiver | pg_stat_get_archiver | (follow-up) |
| pg_stat_wal | pg_stat_get_wal | (follow-up) |

### Per-view catalog rows

The same three-tuple shape applies to every system view. Each
sub-section below names the values that differ.

**pg_class row (relkind='v')**

| Column | Value (all six views) | Notes |
|---|---|---|
| oid | per-view (12100..12106) | unique |
| relname | per-view | matches `system_views.sql` |
| relnamespace | 11 (pg_catalog) | |
| reltype | per-view (composite rowtype OID; see §pg_type below) | non-zero |
| relam | 0 | views have no AM |
| relfilenode | 0 | views have no storage |
| reltablespace | 0 | |
| relpages, reltuples, relallvisible, relallfrozen | 0, -1, 0, 0 | PG18 columns |
| relhasindex | f | |
| relisshared | f | per-database |
| relpersistence | 'p' | |
| relkind | **'v'** | |
| relnatts | per-view (10, 15, 11, …) | matches `pg_attribute` row count |
| relchecks | 0 | |
| relhasrules | **t** | view body lives in `pg_rewrite._RETURN` |
| relhastriggers | f | |
| relhassubclass | f | |
| relrowsecurity, relforcerowsecurity | f, f | |
| relispopulated | t | |
| relreplident | 'n' | |
| relispartition | f | |
| relrewrite | 0 | |
| relfrozenxid | 0 | views have no heap |
| relminmxid | 0 | |
| relacl, reloptions, relpartbound | NULL × 3 | |

`relhasrules = true` is load-bearing: PG's relcache, on opening a
view, issues `SearchSysCache2(RULERELNAME, view_oid, "_RETURN")`
to load the ON-SELECT rule. If `relhasrules` were `false` the
lookup is skipped and the view body is never expanded — the
backend then FATALs on the empty `RelationData->rd_rules` when
the planner tries to expand the RTE. If `relhasrules = true`
but no matching `pg_rewrite` row exists, the lookup hard-FATALs
with `ERROR: rule "_RETURN" for view "pg_stat_wal_receiver" does
not exist`. Both halves must move together.

**pg_attribute rows (one per view column).**

Use the standard 25-column PG18 pg_attribute layout from
`05-local-catalog-bootstrap.md`. Per-column values:

- `attrelid` = view pg_class OID,
- `attname` = column name verbatim from `system_views.sql`,
- `atttypid` = the SRF column's type OID (table below),
- `attlen`, `attbyval`, `attalign` = derived from
  `pg_type.typlen / typbyval / typalign` for that type OID,
- `attnotnull` = false (every projected column is NULLable
  through the `pg_stat_get_wal_receiver` early-out at
  `walreceiver.c:1466`),
- `attstattarget`, `attacl`, `attoptions`, `attfdwoptions`,
  `attmissingval` = NULL.

#### `pg_stat_wal_receiver` (15 cols, view OID 12100)

| # | Name | atttypid | Type | attlen | attbyval | attalign |
|---|---|---:|---|---:|:-:|:-:|
| 1 | pid | 23 | int4 | 4 | t | i |
| 2 | status | 25 | text | -1 | f | i |
| 3 | receive_start_lsn | 3220 | pg_lsn | 8 | t | d |
| 4 | receive_start_tli | 23 | int4 | 4 | t | i |
| 5 | written_lsn | 3220 | pg_lsn | 8 | t | d |
| 6 | flushed_lsn | 3220 | pg_lsn | 8 | t | d |
| 7 | received_tli | 23 | int4 | 4 | t | i |
| 8 | last_msg_send_time | 1184 | timestamptz | 8 | t | d |
| 9 | last_msg_receipt_time | 1184 | timestamptz | 8 | t | d |
| 10 | latest_end_lsn | 3220 | pg_lsn | 8 | t | d |
| 11 | latest_end_time | 1184 | timestamptz | 8 | t | d |
| 12 | slot_name | 25 | text | -1 | f | i |
| 13 | sender_host | 25 | text | -1 | f | i |
| 14 | sender_port | 23 | int4 | 4 | t | i |
| 15 | conninfo | 25 | text | -1 | f | i |

Cite `src/backend/replication/walreceiver.c:1392-1517` for the
column order and types; cite `src/include/catalog/pg_proc.dat:5668-5674`
for the `pg_stat_get_wal_receiver` row that pins `proallargtypes`
and `proargnames` to exactly this sequence.

#### `pg_stat_replication` (20 cols, view OID 12102)

Source: `system_views.sql:906-930`. Joins `pg_stat_get_activity` to
`pg_stat_get_wal_senders` on `pid` and `LEFT JOIN`s `pg_authid` for
`usename`. Columns (abbreviated):

| # | Name | atttypid | Type |
|---|---|---:|---|
| 1 | pid | 23 | int4 |
| 2 | usesysid | 26 | oid |
| 3 | usename | 19 | name |
| 4 | application_name | 25 | text |
| 5 | client_addr | 869 | inet |
| 6 | client_hostname | 25 | text |
| 7 | client_port | 23 | int4 |
| 8 | backend_start | 1184 | timestamptz |
| 9 | backend_xmin | 28 | xid |
| 10 | state | 25 | text |
| 11 | sent_lsn | 3220 | pg_lsn |
| 12 | write_lsn | 3220 | pg_lsn |
| 13 | flush_lsn | 3220 | pg_lsn |
| 14 | replay_lsn | 3220 | pg_lsn |
| 15 | write_lag | 1186 | interval |
| 16 | flush_lag | 1186 | interval |
| 17 | replay_lag | 1186 | interval |
| 18 | sync_priority | 23 | int4 |
| 19 | sync_state | 25 | text |
| 20 | reply_time | 1184 | timestamptz |

#### `pg_stat_recovery_prefetch` (10 cols, view OID 12103)

Source: `system_views.sql:965-977`.

| # | Name | atttypid | Type |
|---|---|---:|---|
| 1 | stats_reset | 1184 | timestamptz |
| 2 | prefetch | 20 | int8 |
| 3 | hit | 20 | int8 |
| 4 | skip_init | 20 | int8 |
| 5 | skip_new | 20 | int8 |
| 6 | skip_fpw | 20 | int8 |
| 7 | skip_rep | 20 | int8 |
| 8 | wal_distance | 23 | int4 |
| 9 | block_distance | 23 | int4 |
| 10 | io_depth | 23 | int4 |

#### `pg_stat_subscription` (11 cols, view OID 12104)

Source: `system_views.sql:979-994`.

| # | Name | atttypid | Type |
|---|---|---:|---|
| 1 | subid | 26 | oid |
| 2 | subname | 19 | name |
| 3 | worker_type | 25 | text |
| 4 | pid | 23 | int4 |
| 5 | leader_pid | 23 | int4 |
| 6 | relid | 26 | oid |
| 7 | received_lsn | 3220 | pg_lsn |
| 8 | last_msg_send_time | 1184 | timestamptz |
| 9 | last_msg_receipt_time | 1184 | timestamptz |
| 10 | latest_end_lsn | 3220 | pg_lsn |
| 11 | latest_end_time | 1184 | timestamptz |

#### `pg_replication_slots` (21 cols, view OID 12105)

Source: `system_views.sql:1019-1043`. PG18 has added
`two_phase_at`, `inactive_since`, `conflicting`,
`invalidation_reason`, `failover`, `synced` (last six entries):

| # | Name | atttypid | Type |
|---|---|---:|---|
| 1 | slot_name | 19 | name |
| 2 | plugin | 19 | name |
| 3 | slot_type | 25 | text |
| 4 | datoid | 26 | oid |
| 5 | database | 19 | name |
| 6 | temporary | 16 | bool |
| 7 | active | 16 | bool |
| 8 | active_pid | 23 | int4 |
| 9 | xmin | 28 | xid |
| 10 | catalog_xmin | 28 | xid |
| 11 | restart_lsn | 3220 | pg_lsn |
| 12 | confirmed_flush_lsn | 3220 | pg_lsn |
| 13 | wal_status | 25 | text |
| 14 | safe_wal_size | 20 | int8 |
| 15 | two_phase | 16 | bool |
| 16 | two_phase_at | 3220 | pg_lsn |
| 17 | inactive_since | 1184 | timestamptz |
| 18 | conflicting | 16 | bool |
| 19 | invalidation_reason | 25 | text |
| 20 | failover | 16 | bool |
| 21 | synced | 16 | bool |

#### `pg_stat_replication_slots` (10 cols, view OID 12106)

Source: `system_views.sql:1045-1059`.

| # | Name | atttypid | Type |
|---|---|---:|---|
| 1 | slot_name | 19 | name |
| 2 | spill_txns | 20 | int8 |
| 3 | spill_count | 20 | int8 |
| 4 | spill_bytes | 20 | int8 |
| 5 | stream_txns | 20 | int8 |
| 6 | stream_count | 20 | int8 |
| 7 | stream_bytes | 20 | int8 |
| 8 | total_txns | 20 | int8 |
| 9 | total_bytes | 20 | int8 |
| 10 | stats_reset | 1184 | timestamptz |

### `pg_rewrite._RETURN` rule tuple

PG stores every view's SELECT body as a single ON-SELECT INSTEAD
rule. The catalog's 8-column PG18 layout
(`pg_rewrite.h:32-45`) is:

| # | Name | atttypid | attlen | notnull | initdb value |
|---|---|---:|---:|:-:|---|
| 1 | oid | 26 | 4 | t | per-rule OID (12101 etc.) |
| 2 | rulename | 19 | 64 | t | `'_RETURN'` |
| 3 | ev_class | 26 | 4 | t | the view's pg_class OID |
| 4 | ev_type | 18 | 1 | t | `'1'` (CMD_SELECT) |
| 5 | ev_enabled | 18 | 1 | t | `'O'` (ALWAYS, origin) |
| 6 | is_instead | 16 | 1 | t | `true` |
| 7 | ev_qual | 194 | -1 | t (BKI_FORCE_NOT_NULL) | `'<>'` |
| 8 | ev_action | 194 | -1 | t (BKI_FORCE_NOT_NULL) | nodeToString-serialised Query (parsed `system_views.sql` body) |

Cite `src/backend/rewrite/rewriteDefine.c:52, :470` (`InsertRule`
builds this same eight-Datum array via `heap_form_tuple` against the
nailed pg_rewrite TupleDesc).

#### `ev_qual` encoding

For an unconditional ON-SELECT rule (which every view body uses)
the qual is the empty node tree literal `'<>'`. This is the same
sentinel `nodeToString(NIL)` produces; PG's reader,
`stringToNode("<>")`, returns NULL. The pg_node_tree column is
varlena, so the on-disk encoding is a 1-byte short varlena header
(`0x07` = `(3<<1)|1`) followed by the three ASCII bytes `'<', '>', '\0'`
— except PG does not emit the trailing NUL inside a text varlena
payload, so the actual length is 2 bytes and the header byte is
`0x05`. See `src/include/varatt.h:191-238` for the 1-byte header
rules.

#### `ev_action` encoding

`ev_action` is the `nodeToString(Query *)`
(`src/backend/nodes/outfuncs.c:805`) of the parsed `CREATE VIEW`
body, after rewriting. For `pg_stat_wal_receiver` the tree is:

```
{QUERY :commandType 1 :querySource 0 :canSetTag true
 :resultRelation 0 :hasAggs false …
 :rtable ({RANGETBLENTRY :alias <> :eref {ALIAS :aliasname "s"
   :colnames ("pid" "status" "receive_start_lsn" …)}
   :rtekind 3 :funcordinality false
   :functions ({RANGETBLFUNCTION :funcexpr
     {FUNCEXPR :funcid 3317 :funcresulttype 2249 …}
     :funccolcount 15 …})})
 :targetList ({TARGETENTRY :expr {VAR :varno 1 :varattno 1
   :vartype 23 …} :resno 1 :resname "pid" …} …×15)
 :jointree {FROMEXPR :fromlist ({RANGETBLREF :rtindex 1})
   :quals {OPEXPR …  /* IS NOT NULL pid */}}}
```

The serialised text is ~5.9 KiB for the wal_receiver view; longer
views (pg_replication_slots, 21 columns) approach 8 KiB. goopg
ships these as embedded `.dat` files
(`internal/initdb/pg_stat_wal_receiver_ev_action.dat` is the first;
see "What goopg must produce" below).

**OID rewriting between PG and goopg.** Within `ev_action` the
RANGETBLENTRY references the **function** (funcid 3317), not the
view's pg_class OID, so a dump captured from a vanilla PG18 cluster
can be embedded verbatim — no per-view OID substitution is required
inside the tree. The view's own OID lives in the *outer*
`pg_rewrite.ev_class` column (column 3), where goopg's local
12100..12106 OIDs are written.

#### Null-bitmap rules for pg_rewrite tuples

All eight columns are `BKI_FORCE_NOT_NULL`
(`pg_rewrite.h:42-43` for `ev_qual` / `ev_action`; the fixed-width
columns are inherently not-null from the CATALOG declaration). The
heap tuple therefore has `HEAP_HASNULL = 0` and no null bitmap;
`t_hoff = MAXALIGN(23) = 24`.

### `pg_stat_get_wal_receiver` SRF (OID 3317) — `pg_proc` row

The `pg_proc.dat` row at `src/include/catalog/pg_proc.dat:5668-5674`:

| Column | Value |
|---|---|
| oid | 3317 |
| proname | pg_stat_get_wal_receiver |
| pronamespace | 11 |
| proowner | 10 |
| prolang | 12 (internal) |
| procost | 1 |
| prorows | 0 |
| provariadic | 0 |
| prosupport | 0 |
| prokind | 'f' |
| prosecdef | f |
| proleakproof | f |
| proisstrict | **f** |
| proretset | **f**  (returns a single composite row, not a set) |
| provolatile | **'s'** (stable) |
| proparallel | 'r' (restricted) |
| pronargs | 0 |
| pronargdefaults | 0 |
| prorettype | 2249 (record) |
| proargtypes | `''` (empty oidvector) |
| proallargtypes | `{23,25,3220,23,3220,3220,23,1184,1184,3220,1184,25,25,23,25}` (15 OUT args) |
| proargmodes | `{o,o,o,o,o,o,o,o,o,o,o,o,o,o,o}` (15 × `'o'`) |
| proargnames | `{pid,status,receive_start_lsn,receive_start_tli,written_lsn,flushed_lsn,received_tli,last_msg_send_time,last_msg_receipt_time,latest_end_lsn,latest_end_time,slot_name,sender_host,sender_port,conninfo}` |
| prosrc | `pg_stat_get_wal_receiver` |
| probin, prosqlbody, proconfig, proacl | NULL × 4 |

The function's implementation is in
`src/backend/replication/walreceiver.c:1392-1517`. It does **not**
read any catalog: it reads `WalRcv` shared memory under
`SpinLockAcquire(&WalRcv->mutex)` (`:1416`), copies 15 fields into a
local `Datum[]`/`bool[]` pair, and calls `heap_form_tuple` against
a composite tuple descriptor derived from `get_call_result_type` —
which in turn is *driven by* the same `pg_proc` row's
`proallargtypes` / `proargnames` arrays. The 15-column shape pinned
in this `pg_proc` row therefore determines the runtime tuple
descriptor that the `_RETURN` rule's `ev_action` must reference.

The matching SRFs for the other five views follow the same
"`pg_stat_get_*` reads shared memory" pattern; see Continuous
maintenance below for the per-SRF source location.

### Composite rowtype OIDs

Each view's `pg_class.reltype` points at a `pg_type` row of
`typtype = 'c'` (composite) whose `typrelid` is the view OID. For
the six replication views these composite types are assigned from
the same 12000..16383 range as the views, one slot apart from each
view's own OID. The composite-type seeding is mechanical and not
elaborated further here; the rows live in
`internal/initdb/pg_type_bootstrap.go`.

---

## Continuous maintenance

A vanilla PG18 backend may attach as a streaming standby at any
point after `initdb`. Every catalog mutation that touches a view or
a rule must therefore (a) write a PG-canonical heap tuple, (b)
emit `XLOG_HEAP_INSERT` / `XLOG_HEAP_UPDATE` / `XLOG_HEAP_DELETE`,
(c) update the `pg_rewrite_rel_rulename_index` (OID 2693), and (d)
fire `CacheInvalidateRelcache(viewRel)` so other backends invalidate
their rule cache.

### DDL operations affecting `pg_rewrite`

| Operation | `pg_class` touch | `pg_attribute` touch | `pg_rewrite` touch | `pg_depend` touch | Citation |
|---|---|---|---|---|---|
| `CREATE VIEW` | INSERT (relkind 'v', relhasrules=t) | INSERT × ncols | INSERT 1 `_RETURN` row via `InsertRule` | INSERT for view→relations referenced in the rule | `src/backend/commands/view.c:356`; `src/backend/rewrite/rewriteDefine.c:470` |
| `CREATE OR REPLACE VIEW` (compatible col list) | UPDATE (HOT, no relfilenode change) | UPDATE / INSERT (added cols) | UPDATE `ev_action` of existing row (no rule-OID change) | refresh | `view.c:356` → `DefineQueryRewrite(replace=true)` (`rewriteDefine.c:224`) |
| `ALTER VIEW … RENAME` | UPDATE `relname` | none | none | none | `src/backend/commands/tablecmds.c:4206, :4270` |
| `ALTER VIEW … RENAME COLUMN` | none | UPDATE `attname` | none | none | `tablecmds.c:4206` |
| `ALTER VIEW … SET SCHEMA` | UPDATE `relnamespace` | none | none | UPDATE | `tablecmds.c:18946` |
| `DROP VIEW` | DELETE | DELETE × ncols | DELETE `_RETURN` row via `RemoveRewriteRuleById` | DELETE × | `src/backend/rewrite/rewriteRemove.c:33` |
| `CREATE RULE` (non-view) | UPDATE `relhasrules=t` | none | INSERT | INSERT | `rewriteDefine.c:190, :470` |
| `ALTER RULE … RENAME` | none | none | UPDATE `rulename` | none | `rewriteDefine.c:793` |
| `DROP RULE` | UPDATE `relhasrules` if last | none | DELETE | DELETE | `rewriteRemove.c:33` |

Every successful mutation above must follow the
`CacheInvalidateRelcache(rel)` / `CacheInvalidateRelcacheByTuple()`
emission documented in `src/backend/utils/cache/inval.c:1635` and
the per-DDL invalidation order documented in
[`05-local-catalog-bootstrap.md`](05-local-catalog-bootstrap.md).
The receiver side consumes these via
`RelationCacheInvalidateEntry` (`inval.c:856`) at
transaction-start.

### Runtime sources for SRF rows

For each view-backing SRF the **artefact to maintain is
shared-memory state, not a catalog tuple**. goopg, when acting as a
primary, must keep the equivalent in-process state populated so a PG
standby's `pg_stat_get_*` SRF returns sane values when a client
queries the view.

| SRF (OID) | Reads from | Goopg artefact |
|---|---|---|
| `pg_stat_get_wal_receiver` (3317) | `WalRcv` (`WalRcvData *`) shared-memory struct under `WalRcv->mutex` spinlock; `WalRcv->writtenUpto` via `pg_atomic_read_u64`. Cite `src/backend/replication/walreceiver.c:1416-1447`; `src/backend/replication/walreceiverfuncs.c:34, :59`. | `internal/wal/replmon.go::Receivers` (200, 206, 244-340) — the in-process registry of one `ReceiverState`. |
| `pg_stat_get_wal_senders` (3099) | `walsnd[]` array (one entry per active walsender). Cite `src/backend/replication/walsender.c:3914`. | `internal/wal/replmon.go::Senders` (59-102). |
| `pg_get_replication_slots` (3781) | `ReplicationSlotCtl->replication_slots[]`. Cite `src/backend/replication/slot.c:144, :211, :224`; `src/backend/replication/slotfuncs.c:236`. | `internal/wal/slots.go::Slots` (86-303, persisted under `pg_replslot/*/state`). |
| `pg_stat_get_replication_slot` (6169) | per-slot pgstat counters. Cite `src/backend/utils/adt/pgstatfuncs.c:2113`. | `internal/wal/slots.go` + pgstats sidecar (not yet wired). |
| `pg_stat_get_subscription` (6118) | logical-replication launcher state. Cite `src/backend/replication/logical/launcher.c:1301`. | `internal/wal/subscriber_mon.go`. |
| `pg_stat_get_recovery_prefetch` (6248) | xlogprefetcher counters. Cite `src/backend/access/transam/xlogprefetcher.c:824`. | none (goopg has no recovery prefetcher; SRF must return a single all-NULL row to match upstream when prefetch is disabled). |

When a vanilla PG standby is replaying goopg-emitted WAL it executes
these SRFs in the standby's own backend; goopg never runs the SRF
body. What goopg must ensure is that the **`pg_proc` rows backing
the SRFs exist with the PG18 `proallargtypes`/`proargnames` arrays**
(Step 3dj / 3dk; see "What goopg must produce") so the standby's
`heap_form_tuple` against `get_call_result_type` agrees with the
in-binary C function signature.

### Cache invalidation on view DDL

`CacheInvalidateRelcache(viewRel)` (`inval.c:1635`) enqueues an
`SHAREDINVALRELCACHE_ID` SI message keyed by the view's pg_class
OID. Every other backend's next transaction-start
`AcceptInvalidationMessages` call routes the message to
`RelationCacheInvalidateEntry` (`inval.c:856`), which both drops
the cached `RelationData` and clears the cached rule list
(`rd_rules` field, rebuilt on next open via `SearchSysCache2(
RULERELNAME, view_oid, "_RETURN")`).

For a `CREATE OR REPLACE VIEW` that only updates `ev_action`, the
rule-cache invalidation alone is insufficient: PG also bumps
`pg_class.reltuples` of `pg_rewrite` (via the HOT-update path) and
invalidates the **rewrite cache** keyed by `RULERELNAME`. goopg's
`internal/executor/operators_ddl.go::execCreateView`
(`:672-701`) currently performs the in-memory catalog update only;
the SI-message + WAL-record half is gap, tracked under "What goopg
must produce".

---

## What goopg must produce

### Initdb-time per-view status

| View | Status | Notes |
|---|---|---|
| pg_stat_wal_receiver | **partial** | pg_class + pg_attribute seeded (Step 3dl, `internal/initdb/relcache_init.go:678`). pg_rewrite TupleDesc fixed to PG18 8-col layout (Step 3dm phase A). `_RETURN` heap tuple in progress: `internal/initdb/pg_rewrite_bootstrap.go` + `internal/initdb/pg_stat_wal_receiver_ev_action.dat` (Step 3dm phase B). pg_proc 3317 row seeded (Step 3dj + 3dk). Remaining: write the heap tuple into `base/{1,5}/2618` and add the `pg_rewrite_rel_rulename_index` leaf entry; verify with the E2E gate. |
| pg_stat_replication | **missing** | pg_class row, 20 pg_attribute rows, pg_rewrite `_RETURN`, plus pg_proc rows for `pg_stat_get_wal_senders` (3099) and `pg_stat_get_activity` (2022) absent. |
| pg_stat_subscription | **missing** | pg_class row, 11 pg_attribute rows, pg_rewrite `_RETURN`, pg_proc row for `pg_stat_get_subscription` (6118) absent. |
| pg_stat_recovery_prefetch | **missing** | pg_class row, 10 pg_attribute rows, pg_rewrite `_RETURN`, pg_proc row for `pg_stat_get_recovery_prefetch` (6248) absent. |
| pg_replication_slots | **missing** | pg_class row, 21 pg_attribute rows, pg_rewrite `_RETURN`, pg_proc row for `pg_get_replication_slots` (3781) absent. |
| pg_stat_replication_slots | **missing** | pg_class row, 10 pg_attribute rows, pg_rewrite `_RETURN`, pg_proc row for `pg_stat_get_replication_slot` (6169) absent. |
| pg_stat_activity, pg_stat_database, pg_stat_bgwriter, pg_stat_archiver, pg_stat_wal | **missing** | Out of scope here; tracked as a follow-up. |

### Files

| File | Purpose | State |
|---|---|---|
| `internal/initdb/pg_rewrite_bootstrap.go` | Schema + entries table for `_RETURN` rule rows | exists; currently one entry for pg_stat_wal_receiver |
| `internal/initdb/pg_stat_wal_receiver_ev_action.dat` | Embedded `nodeToString(Query)` for pg_stat_wal_receiver | exists |
| `internal/initdb/replication_views.go` (**new helper**) | Adds the five remaining replication-view pg_class + pg_attribute entries and their pg_rewrite `_RETURN` rows | to create; should reuse `pg_rewriteInitialEntries()` and append `pgRewriteEntry` rows for OIDs 12102..12106. Embeds five additional `.dat` files (one ev_action per view) captured from a vanilla PG18 dump. |
| `internal/initdb/relcache_init.go` | `nailedLocalRels[]` table of view rows (Step 3dl seeded pg_stat_wal_receiver at line 678) | needs five more entries |
| `internal/initdb/pg_proc_view.go` | SRF `pg_proc` row seeds (Step 3dj/3dk populated 3317) | needs rows for 3099, 6118, 6169, 6248, 3781 with full `proallargtypes` / `proargnames` per `pg_proc.dat` |

### Continuous-maintenance DDL handler

| DDL | Handler | Status | Gap |
|---|---|---|---|
| `CREATE VIEW` | `internal/executor/operators_ddl.go::execCreateView` (`:672`) | **partial** | Inserts into the in-memory `catalog.InMemory.CreateView` only. Missing: emit `XLOG_HEAP_INSERT` for the new pg_class / pg_attribute / pg_rewrite tuples; emit `XLOG_BTREE_INSERT_LEAF` for the `pg_rewrite_rel_rulename_index` (OID 2693); enqueue `CacheInvalidateRelcache` SI message; call equivalent of `InsertRule` (`rewriteDefine.c:52`) to produce the canonical `_RETURN` row. |
| `CREATE OR REPLACE VIEW` | `execCreateView` with `s.OrReplace` (`:701`) | **partial** | Same gap; additionally must perform a HOT update of the existing pg_rewrite row (UPDATE `ev_action`) rather than INSERT. |
| `ALTER VIEW … RENAME` / `RENAME COLUMN` | `internal/executor/operators_ddl.go::execAlterTable` rename branches | **partial** | pg_class.relname / pg_attribute.attname update only; no SI message, no WAL. |
| `DROP VIEW` | `execDropTable` (relkind dispatch) | **partial** | Missing pg_rewrite DELETE (`RemoveRewriteRuleById` equivalent) and pg_depend cleanup; WAL gap. |
| `CREATE RULE` / `DROP RULE` (non-view) | none | **missing** | A goopg primary cannot serve user-defined rules today; this gap also means `relhasrules` on user tables cannot transition. |

### Runtime SRF state surfaces

The SRFs are executed by the **PG standby** running goopg-replicated
WAL. goopg must seed the `pg_proc` rows (above) so the standby's
`get_call_result_type` agrees with the in-binary C signatures; the
**runtime tuple values** that PG returns are pulled from PG-side
shared memory populated by PG-side replication processes (walsender,
walreceiver, slot manager). On a **goopg primary serving a goopg
standby**, the equivalent state lives in goopg's own
`internal/wal/replmon.go::{Senders, Receivers}` and
`internal/wal/slots.go::Slots`; the goopg-side `pg_stat_*` virtual
views (`internal/initdb/replication_views.go`) read those structures
directly and bypass the catalog SRF entirely. Both paths must coexist
because the E2E test exercises a goopg primary → PG standby topology.

### Recommended new helper

A single new file `internal/initdb/replication_views.go` (already
present for the virtual-view path; **extend it** rather than
creating a second file) should expose:

```go
// extends pgRewriteInitialEntries with the remaining 5 replication
// views; each entry embeds an .dat file captured from upstream PG18
// with viewOID rewriting limited to the pg_rewrite.ev_class column.
func replicationViewRewriteEntries() []pgRewriteEntry
```

`pg_rewrite_bootstrap.go::pgRewriteInitialEntries()` then becomes the
concatenation of the wal_receiver entry plus
`replicationViewRewriteEntries()`. Matching pg_class /
pg_attribute / pg_type entries land in
`internal/initdb/relcache_init.go::nailedLocalRels`.

---

## Verification

1. **Catalog shape (psql).** On a goopg cluster with a PG18 standby
   attached:

   ```bash
   psql -h <standby> -c '\d+ pg_stat_wal_receiver'
   psql -h <standby> -c '\d+ pg_stat_replication'
   psql -h <standby> -c '\d+ pg_stat_subscription'
   psql -h <standby> -c '\d+ pg_stat_recovery_prefetch'
   psql -h <standby> -c '\d+ pg_replication_slots'
   psql -h <standby> -c '\d+ pg_stat_replication_slots'
   ```

   must each return the column list defined above (15 / 20 / 11 /
   10 / 21 / 10 columns respectively) with the correct
   types.

2. **SRF return shape.** On the standby:

   ```bash
   psql -c "SELECT * FROM pg_stat_wal_receiver"
   ```

   returns one row with a non-NULL `pid` while the walreceiver is
   running. The probe used by Step 3di — `SELECT status FROM
   pg_catalog.pg_stat_wal_receiver` — must return `'streaming'` (or
   another `WalRcvGetStateString` value, `walreceiver.c:1378-1386`)
   without raising `42P01`.

3. **`pg_filedump` of pg_rewrite.** Inspect the seeded `_RETURN`
   tuples directly:

   ```bash
   pg_filedump -i -f base/1/2618 | grep -E "Item|ev_class|rulename"
   ```

   For each of the six view OIDs there must be exactly one
   `_RETURN` heap tuple with `ev_class` matching the view's
   pg_class OID, `ev_type = '1'`, `ev_enabled = 'O'`, `is_instead =
   t`, `ev_qual = '<>'`, and `ev_action` parseable by
   `stringToNode`. Cross-check with the matching leaf entries in
   `base/1/2693` (`pg_rewrite_rel_rulename_index`).

4. **`stringToNode` round-trip test.** A new
   `internal/initdb/pg_rewrite_ev_action_roundtrip_test.go` reads
   each embedded `.dat` file and asserts that the bytes match the
   `nodeToString(stringToNode(x))` fixed point under PG18 semantics.
   The test is implemented by invoking a small upstream-PG helper
   binary via the goopg test harness (the parse-tree grammar is
   non-trivial to hand-implement in Go).

5. **Negative SI-replay check.** A new
   `internal/executor/operators_ddl_view_invalidation_test.go`
   issues `CREATE OR REPLACE VIEW v AS SELECT 1`, captures the
   emitted SI messages and WAL bytes, and feeds them into a vanilla
   PG18 standby in the E2E harness. After replay, the standby's
   next backend must observe the new view body — `SELECT * FROM v`
   returns the updated projection without any "rule … not found"
   FATAL.

6. **E2E gate.** `TestE2E_FailoverGoopgToPG/async` must no longer
   FATAL on `cache lookup failed for rule "_RETURN" for view
   "pg_stat_wal_receiver"`. The original failure modes from the
   step-3d[i..m] history — `42P01 relation "pg_stat_wal_receiver"
   does not exist` (3di/3dl), `rule "_RETURN" for view … does not
   exist` (3dm), and silent SIGSEGV in `heap_deform_tuple` against
   a mis-typed `pg_rewrite` row (3dm phase A) — must all be cleared
   simultaneously by the row set defined in this file.
