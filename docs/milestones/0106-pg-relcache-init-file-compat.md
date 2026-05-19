# Milestone 0106 — PG Relcache Init File Compatibility

**Status:** planned
**Filed:** 2026-05-17
**Depends on:** M0105 (heap page and tuple format parity)
**Reference plan:** `.ralph/fix_plan.md` (M0106 section)

## Problem

M0105 achieved heap page/tuple format parity and PG standby reaches
PM_HOT_STANDBY. However, PG backends PANIC on startup because critical
system indexes (pg_class_oid_index etc.) cannot be opened. PG's
`RelationBuildDesc` for indexes relies on the **relcache init file** —
a binary file created by PG's `initdb` during the bootstrap phase —
for pre-built relation descriptors of nailed system catalogs and
indexes. `formrdesc()` covers only heap catalogs, not indexes.

Without this file, `RelationIdGetRelation(indexOID)` returns
InvalidRelation, and `load_critical_index()` PANICs. The postmaster
and recovery process reach PM_HOT_STANDBY successfully (no indexes
needed), but no client backend can start.

## Goal

Generate PG-compatible relcache init files during goopg init so PG
backends can start from a goopg-produced backup.

Two files are needed:
1. `global/pg_internal.init` — shared catalogs (pg_database, pg_authid, etc.)
   and their indexes
2. `base/<dboid>/pg_internal.init` — local catalogs (pg_class, pg_attribute,
   pg_type, pg_proc, etc.) and their indexes

## Scope

### In Scope
1. Generate `global/pg_internal.init` with descriptors for shared catalogs
   and indexes (pg_database, pg_authid, pg_auth_members, pg_shseclabel,
   pg_subscription + their indexes)
2. Generate `base/<dboid>/pg_internal.init` with descriptors for local
   nailed catalogs and indexes (pg_class, pg_attribute, pg_type, pg_proc
   + their indexes)
3. Encode RelationData, Form_pg_class, and Form_pg_attribute structs in
   PG-native binary format
4. Verify PG backends can start from a goopg backup (WaitReady passes)
5. Verify full E2E failover test passes

### Out of Scope
- Full pg_class/pg_attribute catalog parity (only nailed relations needed)
- Non-nailed index support
- relcache init file for non-default databases beyond the initial one

## Definition of Done
1. `global/pg_internal.init` created during goopg init
2. `base/1/pg_internal.init` created during goopg init
3. PG standby backends start without PANIC ("could not open critical system index")
4. `SELECT 1` succeeds on PG standby
5. `TestE2E_FailoverGoopgToPG/async` passes
