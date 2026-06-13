Task: M0102-0010 — initdb CLI options. Loop #29 landed the data-page
checksum ENGINE (the reusable, high-blast-radius core); the user-facing
`--data-checksums` initdb option is DEFERRED (Init rejects it). Committed +
pushed → idle on the engine slice.

Files (this loop):
- internal/storage/checksum.go — added PageSetChecksumCopy (copy-then-set,
  never mutates the shared buffer; new page returned verbatim) + VerifyPage
  (checksum half of PageIsVerifiedExtended; new/all-zero page valid).
- internal/storage/smgr.go — ManagerConfig.{ChecksumsEnabled,
  IgnoreChecksumFailure,OnChecksumFailure}; relFile gains checksums/
  ignoreChecksum/onChecksumFail/rel fields (set in relFile()); ChecksumError
  type + verifyOnRead/checksummedForWrite helpers; wired into readBlock,
  writeBlock, extend, extendBatch, engine ReadAt/WriteAt. Disabled = a single
  bool check → byte-identical (no copy/verify/alloc).
- internal/storage/checksum_io_test.go (NEW) — Manager round-trip, corruption
  detect (*ChecksumError), IgnoreChecksumFailure+OnChecksumFailure, disabled=
  byte-identical, ExtendBatch per-block, new-page skip.
- internal/control/pgcontrol.go — ControlFileData.DataChecksumVersion
  (offset 252) decode+encode (preserved across UpdateControlFile).
- internal/initdb/pgcontrol.go — writePgControl/buildPgControl +dataChecksums
  bool; writes 1/0 at offset 252.
- internal/initdb/initdb.go — Options.DataChecksums (seam); Init REJECTS it;
  writePgControl call threads opts.DataChecksums.
- internal/initdb/open.go — reads DataChecksumVersion before NewManager →
  ChecksumsEnabled.
- internal/wal/recovery.go — ReplayFromDir reads DataChecksumVersion (runtime
  uses ReplayFromDirWithMgr w/ already-configured Manager).
- internal/initdb/pg_control_test.go — buildPgControl calls +false arg.
- internal/initdb/data_checksums_test.go (NEW) — buildPgControl 1/0; Init reject.
- docs/design/0102-0019-initdb-data-checksums.md (NEW) + README index row.
- .ralph/{fix_plan.md, deferral_ledger.md} updated.

Next step (next loop = close M0102-0010 user-facing --data-checksums):
route the ~38 direct os.WriteFile bootstrap page-writers through ONE
checksum-aware helper so every bootstrap page gets pd_checksum. Sites
(from the loop-#29 Explore inventory): initdb.go —
bootstrapSharedCatalogPlaceholders (~1225), bootstrapMappedLocalCatalogHeaps
(~1348), bootstrapPostgresRoleWithPassword (~1575), writeMultiPageHeapRows
(~5931/5938, MULTI-PAGE → per-block blkno!), bootstrapPostgresDatabase
(~1694 pg_database + ~50 btree placeholders via makeBtreeRootPage);
btree_index_bootstrap.go — ~30 per-index bootstrappers (metapage+leaf,
multi-page → per-block blkno). bootstrapSystemCatalogs already uses the
Manager (auto-checksummed). Add e2e test: Init(DataChecksums:true) → open a
checksummed Manager → ReadBlock block 0 of pg_type/pg_class/pg_attribute/
pg_database/pg_proc/a btree index (catches any missed site). THEN drop the
Init reject + add `-k`/`--data-checksums`/`--no-data-checksums` CLI flags +
cmd/goopg/main_test.go. Flip default to ON for PG-18 parity LAST (separate
risk; validate recovery+replication first).

Gates run: gofmt clean; go build ./... PASS; go vet storage/control/initdb/
wal PASS; go test ./internal/storage (and -race) PASS; go test -race
./internal/wal PASS; go test ./internal/initdb (103s, full pkg) PASS;
scripts/tpch-spotcheck.sh PASS (Q12=2/Q13=33); make ralph-state-guard
(before status block).
