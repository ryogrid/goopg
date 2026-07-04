(idle — nothing in flight)

---

**Loop #13 addendum (this session).** Concurrent `ralph_loop.sh` peer
detected again at loop start (SessionStart hook); verified via `pgrep -af`
before touching anything. `git status` showed the peer mid-edit on
`internal/executor/pg18_user_catalog_rows.go`/`_test.go` (the
reloptions-heap-encoder gap the prior loop's own addendum had pointed at —
`CheckOption`/`SecurityBarrier`/`SecurityInvoker` round-tripping through
`buildUserPGClassRow`'s `reloptions` column) plus `.ralph/progress.json`/
`weekly_loc.{csv,png}`/an untracked `postgres` dir — none touched.

Picked the *other* M0122-0003 sub-item instead (`pg_stat_io` real data) to
avoid stepping on the peer's file. Landed + pushed `1cba48b9`:
- `internal/executor/pgstat_io.go` (new): ports upstream's
  `pgstat_tracks_io_bktype`/`_object`/`_op` predicates
  (`postgres/src/backend/utils/activity/pgstat_io.c`) into Go
  (`ioTracksObject`/`ioTracksOp`; `ioBackendType` enum lists only the 14
  tracked types, folding `_bktype` in). Verified against a throwaway real
  PostgreSQL 18.3 cluster (`postgres/local_install/bin/{initdb,pg_ctl,psql}`
  against a temp unix-socket data dir — remember
  `LD_LIBRARY_PATH=postgres/local_install/lib` or `psql` fails with a
  `PQsendPipelineSync` symbol-lookup error) rather than derived from static
  C reading alone: `SELECT * FROM pg_stat_io` on a fresh cluster returns 79
  rows across 14 backend types. `fetchIOStatRows(ctx)` builds all 79 with
  upstream's exact NULL-vs-tracked-zero cell shape, overriding only the one
  cell goopg instruments (backend_type='client backend', object='relation',
  context='normal': reads/read_bytes/hits) from the existing
  `storage.Pool.BufferCounters()`. Wired into `valuesOp.Open`
  (`internal/executor/operators.go`, `tbl.Name == "pg_stat_io"` case),
  mirroring the `pg_stat_slru`/`fetchSLRURows` live-data pattern (NOT a
  static `VirtualRows` closure — see [[per_connection_virtual_catalog_scoping]]).
- The same real-PG probe caught a wrong pre-existing test assumption:
  `internal/testport/client_tools_port_test.go`'s
  `TestPort_PgWalsummary002Blocks` asserted 0 `walsummarizer` rows in
  `pg_stat_io` with `summarize_wal=off`; real PG reports 2 (wal/init,
  wal/normal, both all-zero) *unconditionally* —
  `pgstat_tracks_io_bktype` gates on the BackendType enum value, not on
  whether that process type ever actually ran. Fixed the assertion +
  comment to expect 2; PASS.
- Design: `docs/design/0122-0003-explain-format-xml-yaml.md` new
  "pg_stat_io row shape" section + cluster-status table row updated;
  `docs/design/README.md` index entry appended. `.ralph/fix_plan.md`
  M0122-0003 banner updated (also retroactively noted BUFFERS FORMAT
  JSON/XML/YAML as done — landed in an earlier loop, commit `96f390a3`,
  but the banner text was stale).
- New ledger row (status `-`): the other 7 upstream `pg_stat_io` I/O
  counters (writes/extends/evictions/reuses/writebacks/fsyncs + all
  `*_bytes`/`*_time` columns) still render a real 0, not tracked activity —
  same root-cause gap as the BUFFERS `dirtied=`/`written=` rows (no
  write/extend/evict/reuse/fsync instrumentation anywhere in goopg yet).
  `track_io_timing`'s runtime-`SET` gap (recorded earlier) is unaffected.
- Tests: `internal/executor/pgstat_io_test.go` (5 new: row count=79,
  invalid-combination exclusion, NULL/tracked shape, walsummarizer 2-row
  shape, live-counter wiring through `newVMFixture`/`runComposite`/
  `runQueryRows`).
- Gates: `go build ./...`/`go vet ./...` clean; `go test
  ./internal/executor/... ./internal/catalog/...` PASS; `go test -run
  TestPort_PgWalsummary002Blocks ./internal/testport/` PASS;
  `scripts/tpch-spotcheck.sh` PASS this loop (Q12=2 rows/28.7s, Q13=33
  rows/94.8s — the previously-reported hang against the bloated
  `bench/tpch/runtime_goopg/data` dir did NOT reproduce; no action taken).
  Pre-commit pgbench smoke hook PASS at commit time. `make
  ralph-state-guard` found a stale status=running/progress=completed
  mismatch from the *previous* loop's clean-exit marker and auto-repaired
  it to in_progress; re-ran clean.
- Committed via `git add -- <explicit 9 files>` + `git commit -- <same 9
  files>` (never a bare `git add`/pathspec-less `git commit`), per
  `ralph_concurrent_commit_pathspec_required`; verified via `git status`/
  `git show --stat HEAD` before and after that the peer's
  `pg18_user_catalog_rows*.go` files were untouched by this commit.

Next step: pick up the next M0119/M0122 item once the peer's
reloptions-heap-encoder fix lands (check `git log`/`.ralph/deferral_ledger.md`
first — it may already be resolved by the time this file is next read).
Otherwise a fresh `unimplemented_feat.json`/ledger `status=-` row not
touching `pg18_user_catalog_rows.go` or the storage-instrumentation-layer
gap (both `pg_stat_io`'s remaining counters and `track_io_timing` block on
new write/extend/evict/fsync counters — a bigger multi-loop unit, better
picked up deliberately rather than as a side effect of a smaller task).
