Task: M0102-0010 — add the next initdb CLI option. This loop (#21) landed
`-N`/`--no-sync` + `-S`/`--sync-only` (fsync control). Committed → idle on this slice.

Files (this loop):
- internal/initdb/initdb.go — `Options.NoSync`/`Options.SyncOnly`; early SyncOnly
  branch in `Init` (stat dir → "could not access directory" if missing/non-dir,
  fsync, return — before any layout); trailing `fsyncDataDir(abs)` unless NoSync;
  new helpers `fsyncDataDir`/`walkAndFsync`/`fsyncPath`; added `syscall` import.
- internal/initdb/sync_test.go (NEW) — 6 unit tests (sync-only missing-dir reject /
  file reject / existing-dir success+pg_control unchanged / no-sync still creates /
  default syncs cleanly / sync-only follows external pg_wal symlink).
- cmd/goopg/main.go — `-N`/`--no-sync` + `-S`/`--sync-only` flags on `runInit`;
  mode-aware success line ("synced" vs "created").
- docs/design/0102-0012-initdb-sync-options.md (NEW) + README index row.
- .ralph/fix_plan.md (M0102-0010 PROGRESS loop #21; moved --sync-method +
  --no-sync-data-files to remaining/deferred), .ralph/deferral_ledger.md (line),
  .ralph/progress.json (reconciled stale "completed"→"in_progress").

Key facts:
- Mirrors initdb.c sync_pgdata FSYNC method (src/common/file_utils.c walkdir/
  fsync_fname_ext): top-level walk ignores symlinks, recurses through a relocated
  pg_wal symlink separately; fsyncPath tolerates EACCES/EISDIR on open and
  EBADF/EINVAL on dir fsync (some FS reject fsync of O_RDONLY dir). sync_only
  branch mirrors initdb.c:3439-3451 (pg_check_dir<=0 → "could not access directory").
- BEHAVIORAL CHANGE: default `goopg init` now fsyncs (was no fsync at all). Callers
  wanting old speed pass NoSync:true. No on-disk format change.
- Touches only internal/initdb + cmd/goopg — NO executor/planner/catalog/codec, so
  the TPC-H spotcheck gate does NOT apply.
- NOTE: ~19 files of FOREIGN uncommitted changes (internal/{analyzer,catalog,executor,
  mvcc,planner,parser,server} + untracked *_test.go + `postgres`/`validate-ralph-state`)
  are NOT mine (concurrent loop/worktree agents). gofmt -l flags many of THOSE, not
  my files (mine are gofmt-clean). Commit selectively; do NOT `git add -A`.

Next step (next loop): continue M0102-0010 in 001_initdb.pl subtest order. Next
contiguous gap in the "successful creation" block is `--text-search-config` + `--set`
(GUC seeding into postgresql.auto.conf / default_text_search_config). Design doc first.

Gates run: gofmt clean (my files); go vet ./internal/initdb ./cmd/goopg PASS;
go test ./internal/initdb (full pkg) PASS (101s); go build ./... PASS; CLI smoke
(sync-only missing→exit1, --no-sync create→exit0, sync-only existing→exit0,
default fsync→exit0) PASS; make ralph-state-guard PASS.
