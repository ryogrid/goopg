# 0117-0003 — `pg_subtrans` restore-on-restart

Status: accepted
Milestone: M0117-0003 (CLOG ↔ PostgreSQL alignment, gap G5 read path; P1)
Author: Ralph
Date: 2026-06-15

## Problem

goopg tracks subtransaction parentage (sub-XID → parent-XID) in the in-memory
`SubxactMap` (`internal/mvcc/subxact_visibility.go`) and, on the `Manager`, in the
embedded `subxactFields` maps. M0117 added a **write path** to a PG-byte-compatible
`pg_subtrans` SLRU mirror (`internal/mvcc/subxact_slru.go`: `SubtransSLRU.SetParent`,
opted in via `SubxactMap.EnablePersistence`), but:

1. nothing **reads it back** on startup — there is no scan/restore primitive; and
2. the `Manager`'s runtime subxact store (`subxactParents`) is a **separate**
   in-memory map that never touches the SLRU, so the persistence machinery was
   dead code in production (`grep` shows no non-test caller of `NewSubxactMap`
   /`EnablePersistence`).

Consequence: subtransaction parentage is lost across a restart. Per the gap
analysis (`docs/analysis/clog-goopg-gaps-and-remediation-2026-06-14.md` §G5) this
blocks durable subxact resolution for an attached PG standby, two-phase commit,
and any reader that must resolve a subxact's top-level XID after the owning
backend is gone.

This task implements the **read path** (gap G5 read path): scan the on-disk
`pg_subtrans` SLRU back into memory on startup, and wire the `Manager` to use the
persistent `SubxactMap` as its authoritative subxact store when one is attached —
exactly mirroring the `Manager.SetCLog` attachment pattern from M0117-0002.

### Relationship to PostgreSQL `StartupSUBTRANS`

PG itself does **not** restore `pg_subtrans` contents on a clean restart — `subtrans.c`
`StartupSUBTRANS` zeroes the active page because PG only needs subxact parentage for
**in-progress** transactions (a completed top-level transaction's commit/abort status
lives in `pg_xact`, and visibility never needs the subxact link once the parent has
resolved). goopg deliberately diverges here: G5's stated consumers (durable
resolution for an attached standby / 2PC / post-backend-exit readers) need parentage
to survive, so goopg **restores** the persisted links rather than discarding them.
This is a goopg extension, documented as such; it never makes a tuple *more* visible
than the in-memory model would (parentage only redirects a sub-XID to its top-level
ancestor, and the top-level's own commit/abort decision is unchanged), so it is a
conservative addition.

## Design

### 1. Read-back primitive — `SubtransSLRU.ScanParents`

`ScanParents() (map[storage.TransactionID]storage.TransactionID, error)` walks every
`%04X` segment file under the SLRU directory, decodes each 4-byte little-endian slot,
and returns `subxid → parent` for every slot whose parent is a normal XID
(`>= FirstNormalTransactionID`). Slot → XID arithmetic is the exact inverse of
`subtransLocate`:

```
xid = segNo*subtransXactsPerSegment
    + pageInSeg*subtransXactsPerPage
    + xidInPage
```

Zero slots (never-written / the bootstrap-zeroed `0000` segment) are skipped, so a
freshly-initdb'd cluster restores an empty map. Decode is page-structured (a segment
file is a sequence of `BLCKSZ` pages, `subtransXactsPerPage` slots each), matching the
write layout in `SetParent`. A short final page (segment extended only far enough to
hold the highest written slot) is handled by reading whatever bytes exist.

### 2. Restore into the map — `SubxactMap.RestoreFromSLRU`

`RestoreFromSLRU() (int, error)` requires persistence to be enabled (`slru != nil`,
else returns an error so a mis-wired caller fails loudly), calls `ScanParents`, and
merges the result into `m.parents` under the write lock. Returns the number of links
restored. Abort flags are **not** restored — PG's `pg_subtrans` stores only parent
links, and abort status lives in CLOG / in-memory (consistent with `MarkAborted` and
the existing file contract).

### 3. `Manager` attachment — `SetSubxactMap`

A `Manager` gains an optional `*SubxactMap` (`subxactMap`, guarded by the existing
`subxactMu`) and a `SetSubxactMap(*SubxactMap)` setter, mirroring `SetCLog`. When a
map is attached, the `Manager`'s subxact methods use **it** as the authoritative
store instead of the embedded `subxactFields` maps:

| Manager method        | nil (default, v0)          | attached                          |
|-----------------------|----------------------------|-----------------------------------|
| `RegisterSubXid`      | `subxactParents[x]=p`      | `subxactMap.Register` (→ SLRU)    |
| `MarkSubxactAborted`  | `subxactAborted[x]=true`   | `subxactMap.MarkAborted`          |
| `TopLevelXid`         | in-memory parent walk      | `subxactMap.TopLevelXid`          |
| `IsAborted`           | `subxactAborted[x]`        | `subxactMap.IsAborted`            |
| `IsSubxact`           | `subxactParents[x]` exists | `subxactMap.IsSubxact`            |

Selecting one store or the other (rather than consulting both) keeps each path
internally consistent and avoids split-brain between the two maps. With no attachment
every existing caller and test is **byte-identical** to today (the `subxactMap` field
is nil), so this is a strict no-op until `initdb.Open` wires it — the same dormancy
contract M0117-0002 used for `SetCLog`.

`Register` writing through to the SLRU is PG-faithful (`subtrans.c`
`SubTransSetParent` runs at sub-XID assignment) and fsyncs per call; subtransactions
(SAVEPOINT / PL/pgSQL exception blocks) are rare relative to ordinary commits, so the
added latency does not touch the TPC-H / pgbench hot paths (neither issues savepoints).

### 4. `initdb.Open` wiring

After `txnMgr := mvcc.NewManager()` and the existing CLOG wiring, `Open` now:

```go
subxactMap := mvcc.NewSubxactMap()
if err := subxactMap.EnablePersistence(filepath.Join(abs, "pg_subtrans")); err != nil { … }
if _, err := subxactMap.RestoreFromSLRU(); err != nil { … }
txnMgr.SetSubxactMap(subxactMap)
```

`pg_subtrans` already exists as a bootstrapped top-level directory
(`internal/initdb/initdb.go:95`, segment `0000`), so `EnablePersistence` opens it in
place. Restore runs once, early, before any query can register a subxact, so the
restored links are visible to the first snapshot.

## Known limitations / follow-ups

- **No `pg_subtrans` truncation.** Like the pre-G1 CLOG, the mirror grows without
  bound (PG calls `TruncateSUBTRANS` at checkpoint). Negligible at goopg scale
  (4 bytes per *subtransaction* XID, and subxacts are rare); truncation is a
  follow-up coupled to the CLOG-truncation horizon already added in M0117/G1.
- **`SUB_COMMITTED` lane** (the 0x03 CLOG state for a committed subxact whose parent
  is still in progress) is M0117-0004, not this task.

## Testing

`internal/mvcc/subxact_restore_test.go`:

- `ScanParents` on a freshly-opened (empty) SLRU → empty map.
- Write several parent links via `SetParent` spanning >1 page and >1 segment, then
  `ScanParents` → exact `subxid→parent` round-trip (including the cross-segment XID
  arithmetic).
- `SubxactMap.RestoreFromSLRU` with persistence disabled → error (loud mis-wire).
- End-to-end restart: `SubxactMap` A with persistence registers links; a **fresh**
  `SubxactMap` B `EnablePersistence` on the same dir + `RestoreFromSLRU` → `Parent`
  /`TopLevelXid`/`IsSubxact` resolve the same as A.
- `Manager` attachment: `SetSubxactMap` then `RegisterSubXid` → parentage persists to
  SLRU; a fresh `Manager` + restored map resolves `TopLevelXid`; a nil-map `Manager`
  is unchanged (`subxactParents` path still works).

Gates: `go test -race ./internal/mvcc/...`; build; standby-attach E2E
(`TestE2E_PhysicalReplication`) to confirm the `pg_subtrans` wiring does not perturb
the basebackup/standby path. (`pg_subtrans` is zeroed by the standby's
`StartupSUBTRANS`, so the restore is a primary-side concern and the standby path is
unaffected.)

## Upstream references

- `postgres/src/backend/access/transam/subtrans.c` — `SubTransSetParent`,
  `SubTransGetParent`, `StartupSUBTRANS` (the zero-on-startup behaviour goopg diverges
  from), page/segment arithmetic.
- `postgres/src/include/access/slru.h` — `SLRU_PAGES_PER_SEGMENT`.
