Task: M0102-0010 — add the next initdb CLI option. Loop #23 landed
`-g`/`--allow-group-access`. Committed → idle on this slice. This
**completes the entire `001_initdb.pl` "Check group access on PGDATA"
case** (check_mode_recursive($datadir, 0750, 0640) + log_file_mode=0640).

Files (this loop):
- internal/initdb/config_seed.go — `seedPostgresqlConf` gained an
  `allowGroupAccess bool` param; seeds `log_file_mode = 0640` (unquoted,
  replace_guc_value) BETWEEN tsConfig and the -c/--set loop (initdb.c
  setup_config order 1421-1425, so an explicit -c override still wins).
- internal/initdb/initdb.go — `Options.AllowGroupAccess`; new
  `relaxToGroupAccess`/`chmodTreeGroup` (modeled on fsyncDataDir/walkAndFsync:
  top-level walk ignores symlinks, descends a relocated pg_wal symlink
  separately) chmod dirs→0o750 files→0o640; called after full layout but
  BEFORE the trailing fsync so relaxed modes are flushed. seedPostgresqlConf
  call updated to pass opts.AllowGroupAccess.
- internal/initdb/group_access_test.go (NEW) — checkModeRecursive helper
  (Go port of Utils.pm:599, follows pg_wal symlink); TestInitAllowGroupAccess
  (0750/0640 + log_file_mode seed), TestInitDefaultIsOwnerOnly (0700/0600,
  no seed), TestInitAllowGroupAccessWithWALDir (-g + -X relaxes external WAL).
- cmd/goopg/main.go — `-g`/`--allow-group-access` bool flag; threaded into
  initdb.Init Options.
- cmd/goopg/main_test.go — TestInitCommandAllowGroupAccess (full CLI +
  recursive WalkDir mode check + log_file_mode assertion).
- docs/design/0102-0014-initdb-allow-group-access.md (NEW) + README index row.
- .ralph/fix_plan.md (loop #23 progress; removed --allow-group-access from
  remaining-options list).

Key facts:
- PG_DIR_MODE_GROUP=0750, PG_FILE_MODE_GROUP=0640 (file_perm.h:35,41).
  -g calls SetDataDirectoryCreatePerm(PG_DIR_MODE_GROUP) (initdb.c:3360).
- Faithful-but-divergent: PG creates AT group mode; goopg lays out at owner
  mode then relaxes in one pass. Net on-disk tree identical (what
  check_mode_recursive validates). Documented in the design doc.
- Touches ONLY internal/initdb + cmd/goopg — NO executor/planner/catalog/
  codec, so TPC-H spotcheck gate does NOT apply.
- ~19 files of FOREIGN uncommitted changes (internal/{analyzer,catalog,
  executor,mvcc,planner,parser,server} + untracked *_test.go + `postgres`/
  `validate-ralph-state`) are NOT mine. Commit selectively; never git add -A.

Next step (next loop): continue M0102-0010 with the next remaining option.
Suggested next: `--data-checksums` (page-checksum bootstrap — sets the
pg_control data_checksum_version + writes checksummed pages; self-contained
but touches page write path) OR `--auth`/`--pwfile` (pg_hba + bootstrap
password). Design doc first.

Gates run: gofmt clean (my files); go vet ./internal/initdb ./cmd/goopg PASS;
go build ./... PASS; go test ./internal/initdb (full pkg) PASS (105s);
go test ./cmd/goopg -run TestInitCommand PASS; CLI smoke (init -g → dirs 750
/files 640/log_file_mode=0640) PASS; make ralph-state-guard consistent.
