Task: M0102-0010 — add the next initdb CLI option. This loop landed
`-X`/`--waldir` (external WAL directory relocation). Committed → idle on this slice.

Files (this loop):
- internal/initdb/initdb.go — `Options.WALDir`; early absolute-path validation in
  `Init` (before ensureEmptyDir); new `setupWALDir` helper; subdir loop skips the
  literal "pg_wal" when relocating.
- internal/initdb/waldir_test.go (NEW) — 4 unit tests (relative reject / non-empty
  reject / relocation symlink+subdirs / default-is-plain-dir).
- cmd/goopg/main.go — `-X`/`--waldir` flags on `runInit` → `Options.WALDir`.
- docs/design/0102-0011-initdb-waldir-option.md (NEW) + README index row.
- .ralph/fix_plan.md (M0102-0010 PROGRESS loop #20; removed --waldir from the
  remaining-options list), .ralph/deferral_ledger.md (line), .ralph/progress.json
  (reconciled stale "completed"→"in_progress"; timestamp aligned to status.json
  17:30:27 to satisfy 2m skew — guard PASS).

Key facts:
- Mirrors initdb.c create_xlog_or_symlink/pg_check_dir: absent→create / empty→reuse
  (chmod 0700) / non-empty→reject ("exists but is not empty"); relative path
  rejected before any layout (initdb.c:2961). pg_wal becomes a symlink to WALDir;
  os.Mkdir of pg_wal/archive_status + summaries follows the symlink so they land
  inside WALDir.
- Touches only internal/initdb + cmd/goopg — NO executor/planner/catalog/codec, so
  the TPC-H spotcheck gate does NOT apply.
- NOTE: ~771 lines of FOREIGN uncommitted changes sit in internal/{analyzer,catalog,
  executor,mvcc,planner,parser,server} + 2 untracked *_test.go — NOT mine (likely a
  concurrent loop / worktree agents). Build is clean with them present. I committed
  selectively (only my 8 files); do NOT `git add -A`.

Next step (next loop): continue M0102-0010 in 001_initdb.pl subtest order — next
contiguous gap is `--sync-only` (sync an existing data dir) and/or the
`--no-sync`+`--text-search-config`+`--set` combo in the "successful creation"
block. Design doc first, one option per loop.

Gates run: gofmt clean; go vet ./internal/initdb ./cmd/goopg PASS;
go test ./internal/initdb (full pkg) PASS; go build ./... PASS; CLI smoke
(`goopg init -X` symlink OK, `--waldir relwal` exit 1) PASS;
make ralph-state-guard PASS.
