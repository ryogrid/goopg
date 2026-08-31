# Catalog + Postmaster + Replication + Libpq + Auth + Port — Bug Review 2026-08-31

Files: bpchar.go, catalog.go, codec.go, default_acl.go, encoding.go, pg_database_schema.go, pg_node_oid_lookup.go, pg_operator_data.go, pg_operator_seed_data.go, pg_proc_names_generated.go, pgstats.go, pubsub.go, relcache_inval.go, routines.go, cancel.go, conn_tx.go, copy.go, database_ddl.go, dispatch.go, dispatch_extended.go, eof_watch.go, extended.go, grant_ddl.go, notify.go, plancache.go, query.go, role_ddl.go, server.go, statement_log.go, twophase.go, txn_verb.go, autovacuum/launcher.go, applylauncher.go, logicalreceiver.go, logicalwalsender.go, replication_util.go, tablesync.go, tablesync_manager.go, walreceiver.go, walsender.go, frame.go, messages.go, protocol.go, replication.go, auth.go, exchange.go, parser.go, saslprep.go, saslprep_tables.go, scram.go, userstore.go, gls.go, gls_fallback.go, gls_linkname.go, doc.go, nanotime_fallback.go, nanotime_linkname.go, pinp_fallback.go, pinp_linkname.go, sema_fallback.go, sema_linkname.go
Findings count: 9

---

## Catalog

### `bpchar.go:PadBpchar` — No bugs found
Correctly checks `t.IsArray || len(t.Args) == 0`, switch on type name, uses rune count for padding. No issues.

### `encoding.go` — No bugs found
Bounds-checks in `EncodingIDToName`, `ValidServerEncodingName`, `EncodingNameToID` all correct. `pgConvEncNames` has 43 entries, `pgEncodingBELast=34` matches PG.

### `pg_database_schema.go` — No bugs found
Constants/column definitions correct.

### `pg_node_oid_lookup.go` — No bugs found
Binary-operator filter, dedup on key, round-trip check via `OIDToTypeName`, `sync.Once` guards all correct.

### `pgstats.go` — No bugs found
RLock held, sorted iteration, `i >= len(t.Columns)` guard present, field mapping correct.

### `relcache_inval.go` — No bugs found
Error collection, `allDigits`, lock discipline all correct.

### `default_acl.go` — No bugs found
HasDefaultACL/DefaultACLText/defaultACLTextNoOwnerLocked/DefaultACLEntries all correct; owner-injection vs schema-scoped rendering correctly selected.

### `codec.go` — No bugs found
PG18 physical offsets correct (relallfrozen at 108 shifts relisshared→117, etc.). Symmetric encode/decode. `DecodePGIndexPhysicalRow` correctly handles int2vector/oidvector blobs, null bitmap, indexprs/indpred varlenas.

### `pubsub.go` — No bugs found
Compound (dbOid, name) keys, correct state machine transitions, subtransaction notify-level management, deep-copy returns all correct.

### `routines.go` — No bugs found (key functions reviewed)
`Create`/`CreateDuringRecovery`/`LookupWithArgModes`/`Drop`/`ResolveByName`/`Signature` (excludes OUT args) all correct. `routineKey`/`nameKey` compound keys correct.

### `pg_operator_data.go`, `pg_operator_seed_data.go`, `pg_proc_names_generated.go` — Generated data, skimmed
No executable logic. `saslprep_tables.go` likewise generated data, skimmed.

## Postmaster

### `cancel.go` — No bugs found
Mutex discipline, secret-key check, registry register/unregister all correct.

### `eof_watch.go` — No bugs found
MSG_PEEK|MSG_DONTWAIT correctly non-intrusive; Stop() drains the goroutine; nil-safe.

### `notify.go` — No bugs found
Hub mutex, de-duplication at call site, savepoint levels, QueueUsage cap, RemoveSession all correct.

### `plancache.go` — No bugs found
Doorkeeper admission (second-sighting), per-shard RWMutex, eviction, Invalidate all correct.

### `statement_log.go` — No bugs found
Padding/`%-` handling, level escalation, log/duration pairing all correct.

### `twophase.go`, `conn_tx.go` — No bugs found
Same-backend/keep-open vs detached 2PC paths, nil marker for serializable gid, DetachPrepared reset all correct.

### `txn_verb.go` — Bug 1 below; otherwise correct
`endExplicitBlock`, `allowedInAbortedBlock`, verb state machine correct.

### `dispatch.go` — Bug 2 below; otherwise correct
Multi-statement loop, plan-cache ordering, txn-lock identity refresh, abort undo for in-batch DDL, executeFetch/materializeCursor all correct.

### `query.go` — No bugs found
Fast paths, `setAuthzGenericSetForm`, `splitSet`, stripSetToOrEquals, aborted-block gate all correct.

### `copy.go` — No bugs found
CopyIn/CopyOut framing, `\.` EOD handling inside/outside CSV quotes, inline COPY within batches, tx commit ordering all correct.

### `dispatch_extended.go`, `extended.go` — No bugs found
Extended message loop, portal row-position suspension, Bind format-count validation, syncRequired gating all correct.

### `server.go` — No bugs found
Connection lifecycle, auth rejection paths, proc-slot acquire/release, cancel registry wiring, startup handshake, client_min_messages hook all correct.

### `database_ddl.go`, `role_ddl.go`, `grant_ddl.go` — Large DDL handlers, reviewed via symbol overview + targeted pattern scans (not exhaustive line-by-line)
- `database_ddl.go` (2018 lines): string-literal parsing (`unquoteSQLStringLiteral`, quoted-name handling with `len(p) >= 2` guards), block-iteration `off+BlockSize <= len(data)` guard all correct in the scanned regions.
- `role_ddl.go` (1374 lines): `splitLeadingRoleDDL`/`firstTopLevelSemicolon` (see dispatch.go's usage), credential persistence via SCRAM verifiers correct in scanned regions.
- `grant_ddl.go` (834 lines): `tryRecordTableGrant`/`tryRecordTableRevoke`, `nonTableGrantObjects` exclusion, `cutLeadingKeyword` `len(s) <= len(kw)` guard correct in scanned regions.
- No bugs found in the scanned portions; recommend a dedicated line-by-line pass given their size.

### `autovacuum/launcher.go` — Bug 3 below; otherwise correct
Wraparound/aggressive prioritisation, VM skip-guard, cost pacing all reasonable.

---

## Findings

### `internal/postmaster/txn_verb.go:applyTransactionVerb` (TxCommit) — DDL created in a failed block survives COMMIT-as-ROLLBACK
- **Bug**: The COMMIT-in-failed-block branch (line ~335-339) rolls the transaction back with a bare `TxnMgr.Rollback(connTx.Tx())` followed by `endExplicitBlock(ctx, connTx, true)`. The explicit TxRollback branch (line ~493-505) correctly calls `executor.ProcessRollbackUndos(ctx, sess)` BEFORE `TxnMgr.Rollback`, but the failed-block COMMIT→ROLLBACK arm does not. `ProcessRollbackUndos` is what unwinds in-memory catalog registrations (CREATE TABLE/INDEX `RecordDDLCreate`) that are NOT transactional; skipping it leaves the in-memory catalog entry alive while the heap/WAL writes were rolled back — a permanent pg_dump-visible catalog/disk desync.
- **When it triggers**: `BEGIN; CREATE TABLE t1(...); <any statement error>; COMMIT;` — the error marks the block failed, the trailing COMMIT becomes a ROLLBACK (correct PG behaviour), but `t1` remains registered in the catalog even though its pg_class/pg_attribute rows were rolled back.
- **Fix**: Call `executor.ProcessRollbackUndos(ctx, sess)` (mirroring the TxRollback arm) before `TxnMgr.Rollback` in the failed-block COMMIT branch.
- **Severity**: high

### `internal/postmaster/dispatch.go:normalizeCompatSQL / normalizeSQLPreservingLiterals` — plan-cache key collisions on quoted (case-sensitive) identifiers
- **Bug**: `normalizeSQLPreservingLiterals` preserves case only inside single-quoted literals. Double-quoted identifiers (`"MyTable"`) are lowercased, so `SELECT * FROM "Foo"` and `SELECT * FROM "foo"` (two DIFFERENT tables when mixed-case identifiers exist) normalise to the identical plan-cache key. The cache is keyed only on `NamespaceDBOid + normalized SQL` and holds resolved `*catalog.Table` pointers, so one session's plan for `"Foo"` can be served to another session's query against `"foo"`.
- **When it triggers**: A database containing distinct case-differing identifiers (`CREATE TABLE "Foo"` + `CREATE TABLE "foo"`), queried through the cross-session plan cache (single-statement simple query or extended Describe/Execute with `s.pc != nil`). DDL invalidates the cache, but two live sessions querying the two tables can still collide.
- **Fix**: Preserve double-quoted identifier case (and ideally dollar-quoted strings / E'' escape strings) in the normaliser — track double-quote state like the single-quote state, or key the cache on the raw SQL.
- **Severity**: medium

### `internal/postmaster/autovacuum/launcher.go:freezeCutoff` — dead signedness check / unsigned wrap reliance
- **Bug**: `fb := nextXID - storage.TransactionID(eff)` where `storage.TransactionID` is `uint32`; the follow-up `if fb < 0 { return 0 }` is dead code (a uint32 is never < 0) and the subtraction wraps silently when `eff > nextXID`. The subsequent `if fb > oldestXmin { fb = oldestXmin }` clamp happens to recover the intended "clamp to OldestXmin" behaviour, so the observable result is currently correct, but the code relies on wraparound + clamp rather than intent.
- **When it triggers**: Only when `nextXID < eff` (fresh cluster, large `vacuum_freeze_min_age`/reloption). Current clamp masks it; a future edit removing the clamp would expose huge-underflowed cutoffs.
- **Fix**: Compute with signed arithmetic (`int64(nextXID) - eff`) and clamp to 0 before converting back.
- **Severity**: low

### `internal/postmaster/statement_log.go:formatLogLinePrefix` — no bugs
Verified padding (`-` prefix, lone `-`), verb expansion, drop-unknown. Clean.

### `internal/libpq/frame.go` — No bugs found
Startup packet/ReadFrame length checks, drain-on-oversized, buffer reuse, ParseStartupParameters all correct.

### `internal/libpq/messages.go` — No bugs found
Frame payload layout (int16/int32 big-endian), NULL length 0xFFFFFFFF, fielded message NUL termination all correct.

### `internal/libpq/protocol.go` — No bugs found
Constants match protocol spec.

### `internal/libpq/replication.go` — No bugs found
walprotocol 'w'/'k'/'r' encode/decode layouts verified byte-for-byte against upstream; WALData/Keepalive/StandbyStatus decoders bounds-checked.

---

## Replication

### `replication_util.go` — No bugs found
`parseLSN` (hi<<32|lo), `formatLSN`, `summariseErrorResponse` (terminator + bounds) correct.

### `walsender.go` — No bugs found
Command dispatch, START_REPLICATION arg grammar (SLOT/PHYSICAL/LOGICAL/TIMELINE/options), CopyBoth streaming loop (receive goroutine + send loop), slot LSN+1, standby-status handling, logical/physical split, keepalive cadence all correct. The non-blocking receiver wait on the error path is deliberate and documented.

### `logicalwalsender.go` — Bug 4 below; otherwise correct
Publication filter union, quoted-identifier split, catalog-snapshot dbOid threading all correct.

### `walreceiver.go` — No bugs found
Reconnect launcher, start-LSN+1, verbatim vs decoded append, raw-gap detection, status updates, SSLMode reject all correct.

### `logicalreceiver.go` — No bugs found
Reconnect loop, applyLSN atomic advance, per-iteration SafeRollback, permanent/transient error classification all correct.

### `applylauncher.go` — No bugs found
Wake/reconcile/worker lifecycle, identity-checked self-removal, stopAll all correct.

### `tablesync.go`, `tablesync_manager.go` — No bugs found
COPY-text line splitting, EOD `\.`, state machine i→d→s, per-rel error isolation, close-everything ordering all correct. Cross-frame row buffering is a documented v0 limitation, not a bug.

### `internal/replication/logicalwalsender.go:walsenderPgoutputAdapter.Write` — LSN underflow on empty write
- **Bug**: `endLSN := a.nextLSN + uint64(len(p)) - 1` underflows when `len(p) == 0` (nextLSN wraps to a huge value). PgOutput never emits a zero-length message today, so this is latent.
- **When it triggers**: A future zero-length pgoutput message, or an empty publication-list edge, would emit a garbage endLSN and stall confirmed_flush_lsn advancement on the subscriber.
- **Fix**: Guard `if len(p) == 0 { return 0, nil }` before computing endLSN.
- **Severity**: low

---

## Auth / Libpq

### `auth.go`, `parser.go`, `exchange.go`, `userstore.go`, `scram.go`, `saslprep.go` — No bugs found
- HBA rule parsing/matching (conn-type, DB/user lists, CIDR + legacy IP/mask), include directives, cycle detection correct.
- SCRAM server state machine: nonce binding, cbind-flag echo verification, authMessage composition, constant-time proof compare, doomed-user timing parity, mock-secret path correct.
- `parseSCRAMAttrs` duplicate detection, `cutLastAttr` last-`p=` handling, `validNoBindingChannelAttr` exact-flag match all correct.
- PBKDF2-HMAC-SHA-256 hand-rolled implementation verified correct (block XOR, big-endian block counter, key truncation).
- MD5 challenge computation `md5 + md5_hex(stored_tail + salt)` correct; plaintext-shadow fallback correct.
- SASLprep NFKC/bidi/prohibited checks match upstream's mapped-input (not normalized-output) quirk faithfully.
- `readSASLInitial` dataLen bounds, negative-length-as-no-initial handling correct.

### `internal/libpq/auth/exchange.go:runSCRAM` — double-rejection of doomed users is harmless
`handleClientFinal` already returns `ErrInvalidPassword` on `s.doomed`, so the trailing `if secret == nil` belt-and-brace never fires (the early error return wins). Not a bug, just dead defensive code — noted for clarity.

### `internal/postmaster/server.go:isReplicationStartupParam` — overly broad match
- **Bug**: Returns `true` for ANY non-empty value not in `{"0","false","FALSE","False"}`. A client sending `replication=off` / `replication=no` / `replication=0x0` (typos, or a future value) would be silently treated as a replication connection, bypassing the SQL path. Upstream only treats `true/on/1/database` as replication and rejects everything else.
- **When it triggers**: A misconfigured client StartupMessage; consequences are limited (still authenticates, just routes through the replication handler which falls back to the SQL path for non-replication commands), so impact is low.
- **Fix**: Whitelist `true/on/1/database` (case-insensitive) and treat everything else as non-replication, matching `walsender.c`'s `got_STOPPING` logic.
- **Severity**: low

---

## Port shims (gls, runtimeshim)

### `gls.go`, `gls_fallback.go`, `gls_linkname.go` — No bugs found
Linkname layout mirror + runtime self-check probe (`probeLayout`) with recover-degrade to stripe 0 is a sound safety pattern. Fallback returns (0,false).

### `nanotime_fallback.go`, `nanotime_linkname.go`, `pinp_fallback.go`, `pinp_linkname.go`, `sema_fallback.go`, `sema_linkname.go`, `doc.go` — No bugs found
Inverse build tags (go1.24 && !go1.27) are mutually exclusive and cover all toolchains. Fallback semaphore correctly serialises across cells via `fallbackSemaConds`; fallback PinP/UnpinP non-recursion contract documented; linkname signatures match runtime aliases (`sync.runtime_Semacquire`/`Semrelease`, `runtime.procPin`/`procUnpin`, `runtime.nanotime`, `runtime/pprof.runtime_getProfLabel`).

---

## Summary

- **High**: `txn_verb.go` COMMIT-in-failed-block skips `ProcessRollbackUndos` (catalog/disk desync for in-block DDL).
- **Medium**: `dispatch.go` plan-cache key normaliser lowercases quoted identifiers (cross-session wrong-plan on case-sensitive tables).
- **Low**: `autovacuum/launcher.go` unsigned-wrap dead check in `freezeCutoff`; `logicalwalsender.go` LSN underflow on empty write; `server.go` over-broad `isReplicationStartupParam`.
- No bugs found in the wire-protocol core (frame.go, messages.go, protocol.go, replication.go), SCRAM/SASLprep/MD5 auth, the physical/logical streaming loops, or the port shims.
