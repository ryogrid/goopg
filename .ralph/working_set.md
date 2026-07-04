(idle — nothing in flight)

M0119-0004 (materialized-view restart persistence) committed and pushed to
`align-data-structure-with-pg` (`56168032`):
- Root cause: `buildUserPGClassRow` (`internal/executor/pg18_user_catalog_rows.go`)
  hardcoded pg_class.relkind to `"r"`/`"p"` only, so a matview's heap row looked
  like an ordinary table; `loadUserTablesFromHeap` (`internal/initdb/open.go`)
  filtered strictly on `relkind == "r"`, so a restarted matview silently
  downgraded to `IsMatView=false`/`View=nil` (physical data survived, matview-ness
  did not). Fixed relkind='m' round-trip + new `wal.RecordKindCreateMatView`
  (byte 102) + `internal/initdb/matview_ddl_recovery.go` replay, mirroring
  `RecordKindCreateIndex`'s pattern (emitted via `ctx.Pool.LogChangeRecord`, not
  `ctx.WAL.Append`, since the initdb test harness only wires `ctx.Pool`).
  Tests: `internal/initdb/matview_ddl_recovery_test.go`, confirmed RED pre-fix.
  Design: `docs/design/0110-0001-pg-dump-tap-port.md` "Follow-up" section.
- Deferred (ledger row): plain `CREATE VIEW` has ZERO restart persistence at
  all (`execCreateView` never calls `syncTableToCatalogHeap`) — separate,
  larger gap, not attempted.
- Gates: build/vet clean; `internal/wal`+`internal/initdb` PASS (incl. `-race`
  on wal/mvcc); `internal/executor`+`internal/catalog`+`internal/parser`+
  `internal/server` PASS; `scripts/tpch-spotcheck.sh` PASS (Q12=2/Q13=33);
  pgbench smoke via pre-commit hook PASS.

**Concurrency hazard — STILL PRESENT, now 5+ loops (#6,#7,#8,#9,#10), still
non-blocking.** Two independent `ralph_loop.sh --live` trees are both still
running on this exact working tree (Tree A: root `2085426` `SCREEN -dmS
ralph`, detached; Tree B: root `2087325` screen, attached — this session's
own lineage). This loop caught Tree B landing `adfb7dc7`/`14e0be9f` (EXPLAIN
SETTINGS) cleanly mid-session while this loop's own tests ran, and is now
mid-edit on `internal/storage/bufpool.go` (BUFFERS hit/read counters,
unstaged) — left untouched, not staged, not committed. Same precedent as
loops #6-#9: explicit `git add <file list>` (never `-A`/`.`), verified
`git status` showed none of Tree B's files before commit. Sent another
PushNotification (not delivered — Remote Control inactive); did not attempt
any kill. If a future loop finds itself editing a file Tree B is also
mid-edit on, stop and reconcile per the root-0026 precedent rather than
force-add.

Next step: pick up BUFFERS rendering/`pg_stat_io`/`track_io_timing`
(M0122-0003 remainder) ONLY after checking whether Tree B's `bufpool.go`
counters have landed (they cover the same instrumentation-layer gap — avoid
duplicating). Otherwise: the plain-`CREATE VIEW` restart-persistence gap
this loop deferred (`internal/executor/operators_ddl.go`'s `execCreateView`
needs the same `syncTableToCatalogHeap`/relkind='v'/WAL-record treatment
just built for matviews, but views have no physical/pg_attribute-backed
columns — open design question), or continue the M0119-0004 pg_dump 002-010
TAP battery per `.ralph/fix_plan.md`.
