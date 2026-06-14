Task: M0095-0003 (pg_basebackup TAP port) — `--manifest` backup-manifest parity.

LANDED loop #8 (committing): PG-version-2 backup manifest emulation in
internal/server/basebackup.go. After the tar + final progress and before
CopyDone, the server now sends a CopyData('m') begin-manifest marker then the
manifest bytes via CopyData('d') chunks (mirrors backup_manifest.c +
bbsink_copystream). Key symbols: buildBackupManifest, streamBackupManifest,
manifestEntry, manifestChecksumKind (CRC32C default + NONE + SHA224/256/384/512),
emitBaseBackupTar now returns []manifestEntry. CRC32C = LittleEndian bytes of
crc32.Checksum(Castagnoli) (matches PG pg_checksum_final memcpy). pg_wal/* files
omitted from Files[] (tracked via WAL-Ranges). System-Identifier from
initdb.LoadOrCreateSystemID (matches pg_control). SHA-256 Manifest-Checksum over
the prefix.

Test: TestPort_PgBasebackup010Manifest (internal/testport/pgbasebackup_port_test.go)
— runs pg_basebackup WITHOUT --no-manifest, recomputes every CRC32C + the SHA-256
manifest checksum independently, THEN runs upstream `pg_verifybackup -n` which
ACCEPTS the backup. PASS (1.80s). 010 exec + 010 stream still PASS;
go test -race ./internal/server/ green; go build ./... clean.

CONTAMINATION (unchanged from loops #5-7): 18 files modified at identical mtime
2026-06-13 14:28:14 (catalog.go, operators_lockrows.go, parser/ddl.go, planner,
analyzer, dispatch, …) — single foreign WIP snapshot, NOT mine. Do NOT git add -A.
Commit ONLY my files (basebackup.go, pgbasebackup_port_test.go, design doc,
README index, CSV, CSV markdown, fix_plan, working_set).

Next step: M0095-0003 `-X fetch` (WAL-fetch path) is the next committable
increment in uncontaminated files; or MANIFEST_CHECKSUMS SHA-family end-to-end
test. 011/020/recvlogical still blocked on in-place-tablespace BASE_BACKUP +
logical replication protocol. M0110-0003 SQL surface still needs the foreign WIP
stashed/committed by a human (it must edit catalog.go/parser/dispatch).

Gates run loop #8: go test TestPort_PgBasebackup010* PASS; go test -race
./internal/server/ PASS; pg_verifybackup -n PASS; gofmt clean; go build ./... OK;
gen-oracle-port-status regen OK; make ralph-state-guard (before status block).
