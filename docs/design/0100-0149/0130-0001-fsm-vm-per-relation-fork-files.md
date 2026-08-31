# Per-relation FSM/VM fork files — retire aggregate `pg_fsm_state.bin` / `pg_vm_state.bin`

**Status:** accepted
**Date:** 2026-08-09
**Milestone:** M0130 (S1)
**Supersedes:** the aggregate formats in `internal/storage/fsm.go` and `internal/storage/vm.go`

## Problem

goopg stores free-space-map and visibility-map state in two custom aggregate
binary files (`global/pg_fsm_state.bin` with magic `0x66534D31` ("fSM1") and
`global/pg_vm_state.bin` with magic `0x764D5331` ("vMS1")). PG 18.3 expects
per-relation fork files: `<relfilenode>_fsm` and `<relfilenode>_vm` under
`base/<dbOid>/` (or `global/` for shared relations). Without them, PG sees no
FSM for any relation (falls back to sequential scan for free-space search on
insert) and no VM (no index-only scans). This is gap #4 in
`analysis/cluster-dir-level-compat/README.md` §7.1/§7.2.

## Design

### FSM fork (Free Space Map)

PG format (`postgres/src/backend/storage/freespace/freespace.c`): per-relation
`<relfilenode>_fsm` file storing a three-level B-tree of bytes. Each byte
represents one heap page's free-space fraction (0–255 = 0%–100%). Fork number
is `FSM_FORKNUM` (value from `postgres/src/common/relpath.c`).

goopg change:
- `internal/storage/fsm.go`: add a `WriteFSMFork(rfn RelFileNode, fsm *FreeSpaceMap)`
  that writes the PG-format three-level byte B-tree to
  `base/<db>/<relfilenode>_fsm`.
- `internal/storage/smgr.go`: add `FSM_FORKNUM` fork constant; extend
  `ForkNumber` enum and `relPath` to append `_fsm` suffix.
- Checkpoint: `SaveFSM()` in `internal/initdb/open.go` iterates all tracked
  FSMs and calls `WriteFSMFork` per relation.
- Startup: replace the aggregate-bin load with a scan of `_fsm` fork files.
- BASE_BACKUP: `emitBaseBackupTar` in `internal/server/basebackup.go` includes
  `_fsm` files as fork tar entries.

### VM fork (Visibility Map)

PG format (`postgres/src/backend/access/heap/visibilitymap.c`): per-relation
`<relfilenode>_vm` file with one bit per heap page (two bits in VM v2). Fork
number is `VM_FORKNUM`.

Same pattern as FSM: `WriteVMFork`, `VM_FORKNUM`, `_vm` suffix, checkpoint
persistence, startup load, BASE_BACKUP tar entries.

### Retirement

- Stop writing `pg_fsm_state.bin`/`pg_vm_state.bin` after the new fork writers
  are active.
- Delete the aggregate files on next `goopg init` (existing data dirs:
  harmless orphan files; PG ignores them).
- Remove the aggregate format constants (magic bytes, version fields).

## Guards

1. PG 18.3 `pg_ctl start` against goopg data dir logs no fork errors.
2. `pg_basebackup` output contains `_fsm`/`_vm` files.
3. Insert path uses the FSM fork (free-space search works).
4. Existing `internal/storage/fsm_test.go` and `vm_test.go` updated.
5. UNITS + SMOKE green.

## References

- `analysis/cluster-dir-level-compat/README.md` §7.1, §7.2
- `postgres/src/backend/storage/freespace/freespace.c`
- `postgres/src/backend/access/heap/visibilitymap.c`
- `postgres/src/common/relpath.c` — fork name mapping
