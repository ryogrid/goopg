Task: M0095-0003 (pg_basebackup TAP port) — SHA-family backup-manifest
checksum oracle coverage.

LANDED loop #9 (committing): test-only increment hardening loop #8's manifest
work. New TestPort_PgBasebackup010ManifestChecksums in
internal/testport/pgbasebackup_port_test.go — subtests SHA224/SHA256/SHA384/
SHA512 drive `pg_basebackup --manifest-checksums=<algo>` against one live
goopg cluster (one cluster, 4 backups into separate tempdirs). Each subtest:
asserts every Files[] entry's Checksum-Algorithm == requested algo;
independently recomputes the per-file checksum from disk (sha256.Sum224/Sum256,
sha512.Sum384/Sum512, lowercase hex — matches manifestChecksumKind.checksumFile);
recomputes the always-SHA-256 Manifest-Checksum over the doc prefix; runs
upstream `pg_verifybackup -n` which ACCEPTS all four. Added crypto/sha512 import.
Server side already complete (loop #8: checksumFile/algoName in basebackup.go).

Gates run loop #9: go test TestPort_PgBasebackup010ManifestChecksums PASS (1.78s,
4/4 subtests); go test TestPort_PgBasebackup010* PASS (5.84s, no regression);
gofmt clean; go vet ./internal/testport/ clean; go test -c compile OK;
gen-oracle-port-status regen OK; make ralph-state-guard (before status block).

CONTAMINATION (unchanged from loops #5-8): 18 files modified at identical mtime
(catalog.go, operators_lockrows.go, parser/ddl.go, planner/*, analyzer,
dispatch, …) + untracked gen_override_test.go files — a single foreign WIP
snapshot, NOT mine. Do NOT git add -A. Commit ONLY my files: the test file,
docs/test-port CSV + markdown, fix_plan.md, working_set.md.

Next step: M0095-0003 `-X fetch` (WAL-fetch path) is the next FEATURE increment:
parse the BASE_BACKUP `WAL` boolean option (basebackup.go baseBackupOptions +
parseBaseBackupOptionList), then after emitting the data-dir tar append the
in-range WAL segments to the SAME open tar under pg_wal/ with goopg→PG 24-char
segment-name conversion (mirror basebackup.c includewal block, lines 408-520;
reuse the goopg→PG WAL conversion from replication.go replyStartReplication).
011/020/recvlogical still blocked on in-place-tablespace BASE_BACKUP + logical
replication protocol. M0110-0003 SQL surface still needs the foreign WIP
stashed/committed by a human (it edits catalog.go/parser/dispatch).
