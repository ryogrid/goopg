# Milestone 0082 — Per-relation VM / FSM fork files (PG-aligned layout)

**Status:** planned
**Depends on:** M0080-0003 (VM persistence), M0080-0004 (FSM
persistence)
**Drives:** Align goopg's VM / FSM on-disk layout with
PostgreSQL's per-relation fork files
(`base/<DBOid>/<RelOid>_vm`, `_fsm`) so external tooling
(`pg_visibility`, `pg_freespace`, page-image dump utilities)
can read the data without a goopg-specific decoder.

## Context

M0080-0003 / M0080-0004 chose a single global blob per type
(`<DataDir>/global/pg_vm_state.bin`, `pg_fsm_state.bin`) as the
simplest persistence design. PostgreSQL uses per-relation fork
files. Migrating to that layout is a tooling-compatibility win
but invasive (relfile lifecycle, smgr fork integration, vacuum
sequencing).

## Required design docs

- `docs/design/0082-0001-vm-fork-file-layout.md`
  (per-relation file scheme; bit-packing 2-bits-per-page like
  PG; migration story for clusters with the M0080 global blob).
- `docs/design/0082-0002-fsm-fork-file-layout.md`
  (3-level FSM tree like PG, vs the goopg flat array).

## Tasks

Tasks will be detailed when this milestone is picked up. See the
fix_plan.md note at the top of this file.

## Definition of Done (sketch)

- VM / FSM data lives at the PG-aligned paths.
- `pg_visibility` / `pg_freespace` extensions (or their goopg
  virtual-view equivalents) can read the data.
- Migration from M0080's global blob preserves bit/byte content.
- Existing tests (`TestVisibilityMapSaveLoadRoundTrip` etc.)
  adapted or replaced.
