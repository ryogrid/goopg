# M0146-0035: an online TPC-H clone loses database and role `tpch`

Status: open (S2, banner item 2a). Investigation and a diagnostic landed
2026-10-02; the defect has not been reproduced on current code.

## Symptom (2026-09-29)

Clones of the live TPC-H bench cluster made with
`tpch_private_clone_snapshot` (pg_basebackup -X fetch) sometimes started
with only `postgres`, `template0` and `template1`, and rejected role `tpch`.
Other clones taken minutes apart served `tpch`. One arm clone served `tpch`
on its first, crash-recovery start but lost it after a clean stop and
restart.

The on-disk rows were intact in every case. The `global/1262` row has
`xmin = 5`, `xmax = 0`, infomask `0x803` (no hint bits), and xid 5 is
committed in `pg_xact/0000`.

## How startup builds the database and role lists

`initdb.Open` registers user databases and roles from the shared heaps
(`reloadDatabasesFromHeap`, `reloadRolesFromAuthidHeap`). Both go through
`scanCatalogHeapRows`, which has two exits:

- A row that fails to decode is skipped.
- A row that fails `catalogRowLive` is skipped. For a row with no xmax,
  that means `clog.GetStatus(xmin) == Aborted`.

Before any reload runs, Open re-stamps the CLOG from the WAL commit
records in the recovered window. It then runs `MarkUnknownAsAborted`,
which stamps every xid in `[floor, NextXID)` whose lane reads Unknown as
Aborted.

So a clone whose copy of xid 5's lane read Unknown would lose both shared
rows on that start, and the Aborted stamp would make the loss permanent.
This is the only path found that drops a decodable, xmax-free shared row.

## Ruled out (2026-10-02)

- **Decode:** a probe decodes all four `global/1262` rows, from both the
  clone and the source file.
- **xid reuse:** `XidGen.SetNext` is monotonic, so replay cannot move
  NextXID back and reuse xid 5.
- **Redo start point:** BASE_BACKUP ships a pg_control patched to the
  backup-start checkpoint, so redo begins there. Startup never reads
  `backup_label`.
- **Torn `pg_xact` copy:** SLRU pages are rewritten in place, and xid 5's
  old lane value is already committed.
- **Code changes:** none of the startup-reload or CLOG files changed
  between the incident and the investigation.

## Reproduction attempts (all kept `tpch`)

Evidence is in `analysis/m0146/m0146-0035/recon-20261002.txt`.

- Today's arm clone, started, restarted, and restarted again after
  queries in db `tpch`.
- Six online clones of a busy throwaway source, made with
  `clone-harness.sh` (continuous inserts, a checkpoint every 3 s). Each
  clone was started twice.
- The real source itself restarted on 2026-10-01 and still serves `tpch`.

## Diagnostic (landed)

`scanCatalogHeapRows` now logs a WARN for a shared catalog (`DBOid 0`)
in two cases:

- A row that fails to decode (previously skipped in silence).
- A row with no xmax that the liveness filter rejects. The message names
  the catalog, block and slot, xmin, and the CLOG status of xmin.

Ordinary reloads print nothing. The next clone that comes up without
`tpch` will say whether xid 5 read aborted, unknown or committed.

Test: `internal/initdb/shared_catalog_reject_log_test.go`. It patches
template1's `t_xmin` to a normal xid, then scans with that xid aborted
(one WARN expected) and committed (silent).

## Divergence noted

PG renames `backup_label` to `backup_label.old` once recovery completes;
goopg leaves it in place and never reads it. It is harmless while the
shipped pg_control carries the backup-start checkpoint. It is recorded in
the deferral ledger.
