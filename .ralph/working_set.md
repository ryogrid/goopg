Task: M0102-0010 — add the next initdb CLI option. This loop (#22) landed
`-T`/`--text-search-config` + `-c`/`--set` (postgresql.conf GUC seeding).
Committed → idle on this slice. **This completes the 001_initdb.pl
"successful creation" option set** (--no-sync + --text-search-config + --set
+ --waldir all now accepted).

Files (this loop):
- internal/initdb/config_seed.go (NEW) — `GUCSetting{Name,Value}` type;
  `seedPostgresqlConf(abs,tsConfig,extra)` reads the just-written
  postgresql.conf, applies `default_text_search_config=pg_catalog.<cfg>`
  then each --set in order, writes back 0o600; faithful `replaceGUCValue`
  port (skip leading #/ws, case-insensitive name=, rewrite in place
  preserving canonical casing + inline comment, else append) +
  `formatGUCValue`/`gucValueRequiresQuotes` (mirror initdb.c:526/644).
- internal/initdb/initdb.go — Options.TextSearchConfig + Options.ExtraGUC;
  `seedPostgresqlConf` call right after the SampleFiles() write loop
  (before bootstrapSystemCatalogs / final fsync).
- internal/initdb/config_seed_test.go (NEW) — replaceGUCValue table,
  formatGUCValue table, Init-level seeding checks (the full 001_initdb.pl
  case: -T german + --set default_text_search_config=german → single
  unquoted `german`).
- cmd/goopg/main.go — `-T`/`--text-search-config` string flag + `gucFlag`
  (flag.Value collecting repeated `-c`/`--set NAME=VALUE`; missing `=` →
  deferred err → exit 2 `-c <v> requires a value`).
- cmd/goopg/main_test.go — TestInitCommandSeedsGUCs (full CLI command) +
  TestInitCommandSetRequiresValue (error path, nothing laid out).
- docs/design/0102-0013-initdb-config-seeding.md (NEW) + README index row.
- .ralph/fix_plan.md (M0102-0010 loop #22 progress; removed
  --set/--text-search-config from the remaining-options list).

Key facts:
- Upstream order: -T sets default_text_search_config first, --set applied
  LAST so it overrides (initdb.c:1343-1346 then 1430-1436). That is why the
  test sets both and expects bare `german`, not `pg_catalog.german`.
- Touches only internal/initdb + cmd/goopg — NO executor/planner/catalog/
  codec, so the TPC-H spotcheck gate does NOT apply.
- NOTE: ~19 files of FOREIGN uncommitted changes (internal/{analyzer,catalog,
  executor,mvcc,planner,parser,server} + untracked *_test.go + `postgres`/
  `validate-ralph-state`) are NOT mine (concurrent loop/worktree agents).
  Commit selectively; do NOT `git add -A`.

Next step (next loop): continue M0102-0010 with the next remaining option.
Suggested next: `--allow-group-access` (0o750 dir / 0o640 file mode — small,
self-contained, mirrors initdb.c pg_dir_create_mode/pg_file_create_mode +
the log_file_mode=0640 setup_config tweak). Design doc first.

Gates run: gofmt clean (my files); go vet ./internal/initdb ./cmd/goopg PASS;
go build ./... PASS; go test ./internal/initdb (full pkg) PASS (100s);
go test ./cmd/goopg -run TestInitCommand PASS; CLI smoke (seed both GUCs →
exit0 with correct lines; --set bogus → exit2 no layout; -h lists flags)
PASS; make ralph-state-guard (pending).
