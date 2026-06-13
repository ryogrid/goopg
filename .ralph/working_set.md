Task: M0102-0010 — user-facing initdb `--data-checksums`. Loop #30 landed the
**bootstrap checksum routing primitive** (`checksumRelationData`). Engine
(loop #29) + primitive (loop #30) done; the ~50-site sweep + reject-drop + CLI
flag + default-flip remain (deferral ledger 2026-06-13).

Files (this loop — purely additive, byte-identical to current behavior):
- internal/initdb/checksum_bootstrap.go (NEW) — `checksumRelationData(raw,
  enabled)`: identity (no copy/alloc) when disabled; else per-BlockSize-block
  pd_checksum, blkno derived from byte offset (off/BlockSize, matches smgr
  read-verify) → ONE helper for single-page heaps, multi-page heaps, multi-page
  btree files with NO per-site block bookkeeping. Built on loop-#29
  storage.PageSetChecksumCopy; skips new/all-zero pages (PageIsNew).
- internal/initdb/checksum_bootstrap_test.go (NEW) — 5 cases: disabled=identity
  (same backing slice), enabled-no-input-mutation, per-block verify +
  transposition rejection, new-page skip, partial-tail-verbatim. PASS.
- docs/design/0102-0019-initdb-data-checksums.md — added "Routing primitive"
  + "Remaining (the sweep)" sections. README index already has 0102-0019.
- .ralph/fix_plan.md, .ralph/deferral_ledger.md — loop #30 progress recorded.

⚠️ WORKING-TREE CONTAMINATION (surface to next loop / human):
The tree carries ~18 uncommitted modified files + 2 new test files for an
UNRELATED feature, M0100-0010 (PARTITION OF `WITH OPTIONS GENERATED ALWAYS AS`
column overrides) — parser/ast.go, parser/ddl.go, executor/*, planner/*,
catalog/*, analyzer/*, mvcc/*, server/dispatch.go + parser/gen_override_test.go
+ executor/partition_gen_override_test.go. Last modified 14:28 (8h before this
loop), almost certainly from a separate long-running `claude` session (PID
2177381, started 14:10). Tree builds clean WITH them. Loop #29 already committed
the checksum engine alongside them via selective staging. I did NOT touch them.
DO NOT sweep them into a checksum commit; the M0100-0010 owner should commit
them, or they should be reviewed/reverted.

Next step (next loop): the ~50-site bootstrap sweep — thread `opts.DataChecksums`
into bootstrapPostgresDatabase (initdb.go 1602-2063), bootstrapSharedCatalog-
Placeholders, bootstrapMappedLocalCatalogHeaps, bootstrapPostgresRoleWithPassword,
writeMultiPageHeapRows, and btree_index_bootstrap.go (~40 fns) +
pg_tablespace_bootstrap.go + pg_proc_proname_args_nsp_index_bootstrap.go; wrap
each `os.WriteFile(path, data, 0o600)` as
`os.WriteFile(path, checksumRelationData(data, dataChecksums), 0o600)` (copy-
loops base/1→base/5/template0 preserve block-0 checksums automatically). Add e2e
`Init(DataChecksums:true)`→open checksummed Manager→ReadBlock 0 of
pg_type/pg_class/pg_attribute/pg_database/pg_proc + a btree index (catches any
missed site). THEN drop the Init reject + add `-k`/`--data-checksums`/
`--no-data-checksums` CLI flags + cmd/goopg/main_test.go. Flip default ON LAST
(validate recovery+replication first). Byte-identical while reject stays → safe.

Gates run: gofmt clean; go build ./internal/initdb PASS; go vet ./internal/initdb
PASS; go test ./internal/initdb (104s, full pkg) PASS; checksum unit tests PASS.
⚠️ make ralph-state-guard FAILED — pre-existing state-file inconsistency NOT
caused by this loop: status.json=running(loop30) vs progress.json=completed
(loop#29); driver's loop-start progress→running reset didn't fire. Fix: reset
.ralph/progress.json status to "running" (or re-run driver loop-start), then
re-run the guard. No code/codec/executor/planner change this loop, so TPC-H
spotcheck not required.
