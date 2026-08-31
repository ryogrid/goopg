# Catalog + Postmaster + Replication + Libpq + Auth + Port + PL/pgSQL — Code Review 2026-08-31

Files: catalog (14): bpchar.go, catalog.go, codec.go, default_acl.go, encoding.go, pg_database_schema.go, pg_node_oid_lookup.go, pg_operator_data.go, pg_operator_seed_data.go, pg_proc_names_generated.go, pgstats.go, pubsub.go, relcache_inval.go, routines.go
postmaster (18): cancel.go, conn_tx.go, copy.go, database_ddl.go, dispatch.go, dispatch_extended.go, eof_watch.go, extended.go, grant_ddl.go, notify.go, plancache.go, query.go, role_ddl.go, server.go, statement_log.go, twophase.go, txn_verb.go, autovacuum/launcher.go
replication (8): applylauncher.go, logicalreceiver.go, logicalwalsender.go, replication_util.go, tablesync.go, tablesync_manager.go, walreceiver.go, walsender.go
libpq (4): frame.go, messages.go, protocol.go, replication.go
libpq/auth (7): auth.go, exchange.go, parser.go, saslprep.go, saslprep_tables.go, scram.go, userstore.go
port/gls (3): gls.go, gls_fallback.go, gls_linkname.go
port/runtimeshim (7): doc.go, nanotime_fallback.go, nanotime_linkname.go, pinp_fallback.go, pinp_linkname.go, sema_fallback.go, sema_linkname.go

Generated/seed data and shim files skimmed (noted inline): pg_operator_seed_data.go, pg_proc_names_generated.go (generated tables), saslprep_tables.go (generated ranges), gls/runtimeshim linkname+fallback pairs (thin wrappers).

Findings count: 13

---

### `encoding.go:EncodingNameToID` — Re-computes `cleanConvEncodingName` on canonical names inside a loop on every call
- **Issue**: The canonical fast path iterates `pgConvEncNames` and calls `cleanConvEncodingName(n)` on every canonical name for every lookup; the alias path then does another linear scan of `pgConvEncNames` comparing `n == canonical`. `cleanConvEncodingName` allocates a new `[]byte`/string per call.
- **Why**: `EncodingNameToID` is called from `ValidServerEncodingName` on every encoding-name resolution (CREATE DATABASE, CREATE CONVERSION, pg_encoding_to_char); all ~43 canonical cleaned names are recomputed each call.
- **Suggestion**: Precompute a `map[string]int32` of cleaned canonical name → ID (via `sync.Once`) and look up `key` directly; also build a reverse alias map name → ID so the alias path is O(1) instead of scanning all 43 names.
- **Severity**: low

---

### `catalog.go:PGStatsRowsForDBOid` / `PGClassRowsForDBOid` — `c.ns(dbOid)` re-resolved on every loop iteration
- **Issue**: Both builders call `c.ns(dbOid)` once per table inside loops (`c.ns(dbOid).tables[k]`, `c.ns(dbOid).byTable[t.OID]`), and `PGClassRowsForDBOid` calls it 4× per table just to size the `out` slice. Each call is a map lookup + potential miss fallback.
- **Why**: Called on every pg_stats/pg_class materialization; the namespace resolution result is a loop invariant.
- **Suggestion**: Hoist `ns := c.ns(dbOid)` once at function top and index into it (`ns.tables[k]`, `ns.byTable[...]`).
- **Severity**: low

---

### `catalog.go:InheritanceChildren` / `PartitionChildren` — O(children × tables) lookup by scanning the whole namespace map per child
- **Issue**: For each child OID these loop over **every** table in the namespace (`for _, t := range c.ns(...).tables`) to find the one whose OID matches. With many tables this is quadratic.
- **Why**: These run during inheritance/partition expansion in the planner on the hot path.
- **Suggestion**: There is already an OID-keyed index (`c.ns(dbOid).byTable`); look up `byTable[oid]` directly instead of scanning.
- **Severity**: medium

---

### `catalog.go:RoleIsMemberOf` / `IsAdminOfRole` / `HasPrivsOfRole` / `SelectBestAdmin` — BFS re-scans the entire `roleMembers` map per queue level (O(V×E))
- **Issue**: Each BFS level iterates the whole `c.roleMembers` map filtering by `MemberOID == cur`; with the queue at depth d and E memberships the cost is O(d×E), and `IsSuperuser` scans `c.roles` by OID linearly (O(roles) per call).
- **Why**: These are on the ACL/membership hot path (GRANT checks, role membership resolution), repeatedly invoked per statement.
- **Suggestion**: Maintain a reverse index `map[uint32][]uint32` (member → direct parent roles) updated alongside `roleMembers`, and an OID→name/attrs reverse map for `IsSuperuser`; then BFS walks only the member's edges.
- **Severity**: medium

---

### `catalog.go:LookupTableByOIDAllDBs` / `tableByOID` — linear scan of all tables per OID lookup
- **Issue**: `tableByOID` scans the whole `tables` map comparing `t.OID == oid`; `LookupTableByOIDAllDBs` repeats it per namespace. The `byTable` index (keyed by OID) already exists but is not used here.
- **Why**: `oid::regclass` rendering and replay passes do many OID lookups.
- **Suggestion**: Use `c.ns(dbOid).byTable[oid]` for the single-DB case and iterate namespaces for the AllDBs case.
- **Severity**: medium

---

### `catalog.go:PGClassRowsForDBOid` and other virtual-row builders — `fmt.Sprintf("%d", …)` for every numeric cell
- **Issue**: The pg_class/pg_constraint/pg_index/pg_stat_* builders convert every OID/count to a string with `fmt.Sprintf("%d", …)` (70+ call sites in catalog.go), which is slower and allocates more than `strconv.FormatUint`.
- **Why**: These builders run for every catalog virtual-table scan.
- **Suggestion**: Use `strconv.FormatUint(uint64(v), 10)` (or `strconv.Itoa` for ints) in the hot row builders.
- **Severity**: low

---

### `routines.go:LookupByOID` — linear OID scan of all routines per call
- **Issue**: `LookupByOID` iterates every `byKey` entry comparing `r.OID == oid` — no OID→routine index (unlike `heapTIDs`, which IS OID-keyed). Called from pg_proc virtual-row rendering per routine row.
- **Why**: pg_proc scans call LookupByOID per row; with many routines this is O(n) per row → O(n²) total.
- **Suggestion**: Add a `map[uint32]*Routine` OID index maintained on Create/Rename/SetSchema/Drop (or reuse `heapTIDs` keys to short-circuit misses).
- **Severity**: low

---

### `pubsub.go:pubMapKey`/`subMapKey` — `fmt.Sprintf` + re-lowercasing on every map access
- **Issue**: Every publication/subscription lookup, create, drop allocates a formatted `"%d.%s"` string and re-lowercases `name` on each call; `findSubByName` is an O(n) linear scan used by every `AddSubscriptionRel`.
- **Why**: Called on each DDL statement touching a publication/subscription and on every tablesync state transition.
- **Suggestion**: Key maps directly by a `struct{dbOid uint32; name string}` (lowercased once at insert) to avoid the string build, or cache the lowercased key on the struct.
- **Severity**: low

---

## Postmaster

### `query.go:handleQuery` — redundant string normalizations per simple-query message
- **Issue**: Each simple Query computes `strings.TrimSpace`, `strings.TrimRight(trimmed, ";")`, `strings.TrimSpace` again, `strings.ToUpper(matchable)`, and later `strings.TrimSpace(sql)` inside `logStatement`/`logDuration`. The statement text is transformed repeatedly (trim/upper) across a half-dozen passes, several producing new strings.
- **Why**: This is the hottest path in the server (every simple-protocol statement). The uppercased+trimmed forms are recomputed and held simultaneously.
- **Suggestion**: Compute `upper` once (the only case-sensitive use of `matchable` is `strings.Contains(matchable, ";")`, which can run on the original), reuse one normalized form, and pass the already-trimmed string to the logging helpers instead of re-trimming.
- **Severity**: low

---

### `statement_log.go:leadingKeyword` — re-slices/trims the SQL on each classification
- **Issue**: `leadingKeyword` repeatedly assigns `s = strings.TrimSpace(...)` while skipping comments, and `shouldLog`/`logStatement` call it only after `lvl != logStmtAll`. When logging is off or at `all`, `leadingKeyword` isn't reached, so this is bounded — but the comment-skip loop does `TrimSpace` + `IndexByte`/`Index` per hop.
- **Why**: Per-statement logging path.
- **Suggestion**: For typical single-line statements, do one `strings.IndexAny(s, " \t\n\r(")` scan without TrimSpace churn; acceptable as-is since it only runs when logging is enabled and level != all.
- **Severity**: low

---

### `cancel.go` / `eof_watch.go` / `plancache.go` / `notify.go` / `twophase.go` / `txn_verb.go` — no significant waste found
- `cancel.go`: small lock-scoped registry, fine. `eof_watch.go`: 500 ms poll goroutine with MSG_PEEK, acceptable. `plancache.go`: sharded cache with doorkeeper admission — well-optimized. `notify.go`: mutex-guarded maps; `QueueUsage`/`hasAnyListener` iterate all channels but only at command boundaries. `twophase.go`: low-frequency 2PC ops. `txn_verb.go`: per-COMMIT deferred checks only, fine.
- **Severity**: n/a

---

### `autovacuum/launcher.go:tick` / `needsVacuum` / `needsAnalyze` — `params()` GUC-parse recomputed per table, `NextXID` re-read per table
- **Issue**: `tick` computes `p := l.params()` (parsing 13 GUC values with `strconv`) once, but then `needsVacuum(tbl)` and `needsAnalyze(tbl)` each call `l.params()` **again** per table — a full GUC re-parse (with `strings.TrimSpace`+`ParseInt`/`ParseFloat`) 2× per table, once per tick. `needsVacuum` also re-runs `l.wraparound(tbl, p)` even though `tick` already computed `wrap` for the same table.
- **Why**: Autovacuum tick runs every 60 s over all user tables; with many tables the per-table GUC re-parse is pure duplicate work.
- **Suggestion**: Thread the already-computed `avParams` into `needsVacuum`/`needsAnalyze` (add a `p avParams` param) and pass the precomputed `wrap`/`enabled` flags instead of re-deriving them.
- **Severity**: low

---

### `dispatch.go` / `dispatch_extended.go` / `extended.go` / `server.go` / `copy.go` / `role_ddl.go` / `database_ddl.go` / `grant_ddl.go` — no significant waste found on hot paths
- `dispatch.go` already uses per-connection DataRow scratch buffers (`w.DataRowScratch`) and the plan cache; row rendering is allocation-conscious. `dispatch_extended.go`/`extended.go` use a small `payloadReader` and plan-cache fast path. `copy.go` buffers lines in `lineBuf` (single reallocatable buffer). `role_ddl.go`/`database_ddl.go`/`grant_ddl.go` are DDL-frequency (not hot); their `strings.ToLower`/`Sprintf` usages are per-statement, negligible. `server.go` `runPostStartupLoop` is a straightforward frame dispatch.
- Minor: `grant_ddl.go:tryRecordTableGrant` lowercases the statement twice (`strings.ToLower(stmt)` then per-clause `strings.ToLower(rolePart)` slices) — DDL-frequency, negligible.
- **Severity**: n/a

---

## Replication

### `replication_util.go` / `applylauncher.go` / `tablesync.go` / `tablesync_manager.go` / `walreceiver.go` / `logicalreceiver.go` / `logicalwalsender.go` / `walsender.go` — no significant waste found
- `replication_util.go`: small helpers (`parseLSN`, `formatLSN`, `summariseErrorResponse`), fine.
- `applylauncher.go`: ticker-driven reconcile loop, fine.
- `tablesync.go`/`tablesync_manager.go`: per-rel COPY exchange, DDL-frequency.
- `walreceiver.go`/`logicalreceiver.go`: network I/O bound; frame payloads are copied for channel safety (necessary). `logicalreceiver.go:handleCopyData` uses a CAS loop for `applyLSN` update — correct and fine.
- `logicalwalsender.go`: `splitPublicationNames` allocates a `strings.Builder` per name + `[]rune` conversion — necessary for the identifier unquoting logic.
- `walsender.go` `HandleCommand` trims/uppercases the query string once; fine.
- **Severity**: n/a

---

## Libpq

### `messages.go:WriteStartupMessage` — redundant double-buffer allocation per startup
- **Issue**: The startup packet is built in `body`, then `copy`d into a freshly-allocated `pkt` (`pkt := make([]byte, 4+len(body))`), when the 4-byte length could simply be written first and `body` written directly.
- **Why**: One allocation + one memcpy per connection startup (cold path — but trivially avoidable).
- **Suggestion**: Write the 4-byte length prefix with `fw.w.Write` then `body`, or prepend the length into a single buffer via `slices.Insert`/`append`.
- **Severity**: low

### `frame.go` / `protocol.go` / `replication.go` / `messages.go` (rest) — no significant waste
- `FrameReader` reuses its payload buffer (documented, callers copy when needed). `DataRowScratch`/`PutDataRowScratch` already amortise per-row allocation. `WriteRowDescription`/`WriteDataRow` pre-size their payloads. `replication.go` encoders pre-size output. All good.
- **Severity**: n/a

---

## Auth

### `auth.go:Method.String` / `ConnType.String` — linear map scan per error-message call
- **Issue**: `Method.String()` and `ConnType.String()` iterate the entire `methodNames`/`connTypeNames` map to find the key for a value. Only used in error messages, so severity is low.
- **Suggestion**: Replace the maps with a `switch` (or a value-indexed array) since the enum space is fixed.
- **Severity**: low

### `exchange.go` / `parser.go` / `userstore.go` / `scram.go` / `saslprep.go` — no significant waste
- `exchange.go` reads/copies SASL payloads only when needed; `parser.go` is one-time file parsing; `userstore.go` is map-locked; `scram.go`'s PBKDF2 and per-attr map are per-auth-exchange (rare); `saslprep.go`'s ASCII fast path avoids all the range scans. `saslprep_tables.go` is generated range tables (skimmed — data only).
- **Severity**: n/a

---

## Port (gls / runtimeshim)

### `gls.go` / `gls_fallback.go` / `gls_linkname.go` / `runtimeshim/*` — no significant waste
- All four gls files and all seven runtimeshim files are linkname/fallback shim wrappers around single runtime calls (`BackendID` = one label-map scan of a 1-entry slice, `Nanotime` = one MOV, `SemaAcquire/Release`, `PinP/UnpinP`). The linkname paths are the deliberately-optimised hot paths; the fallbacks are explicitly slow-but-correct. `doc.go` is documentation.
- One minor note: `gls_linkname.go:readLabelID` rescans the whole (typically 1-entry) label slice on every `BackendID` call, and `probeLayout` round-trips through a goroutine at init — both negligible.
- **Severity**: n/a

---

