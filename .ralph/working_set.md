(idle — nothing in flight)

Last loop (#106): **M0131-S1 — GUC registry accepts a PG-18-initdb
`postgresql.conf`** — DONE and ticked. The banner flipped to M0131 (user
directive 2026-08-11), so the baton's M0119 suggestion was correctly overridden.

M-NIGHTLY duty: `ci/logs/action-items.md` still run `20260811-014635` (12 items,
unchanged since loop #100). All 12 already filed; the 11 open ones stay PARKED.

What landed: **ten** accepted-stub GUCs (not the designed 8) in
`internal/config/defaults.go` `BuildDefaultRegistry` + ten commented entries in
`internal/config/postgresql.conf.sample`; new `checkFileMode` CheckFn; new
`internal/config/pg_initdb_conf_test.go` (5 tests); two
`internal/initdb/config_seed_test.go` expectations updated.

Design-table corrections found against `guc_tables.c`: `log_file_mode` BootVal is
`0600` not `0640` (`0640` is what initdb writes under `-g`); the Linux DSM enum
is `posix|sysv|mmap`. `log_file_mode` is `TypeString`+CheckFn, not `TypeInt`,
because `parseIntWithUnit` is base-10 and there is no octal display hook —
ledgered.

Guard 3 proven end-to-end: `goopg init --lc-messages=C -T english -g -D /tmp/s1data`
then `goopg start --listen 127.0.0.1:5539` **starts** (exit 1 before this slice);
`SHOW` returns `lc_time=C`, `log_file_mode=0640`,
`default_text_search_config=pg_catalog.english`, `lc_messages=C`. Server stopped.

Key discovery for later M0131 slices: the initdb-authored portion of the real
reference conf now applies with zero errors, but the WHOLE file still fails on
`unix_socket_directories` / `maintenance_work_mem` / `wal_compression` — all
under `CUSTOMIZED OPTIONS`, i.e. bench-script additions, not initdb output. S3's
E2E uses a pristine initdb dir and will not see this; a real-world dir would.

**Orphan noted:** something is already listening on 127.0.0.1:5533 (an older
goopg build — it answered `SHOW lc_time` with "unrecognized configuration
parameter"). Not started by this loop; worth reaping.

Next loop: per banner — M-NIGHTLY filing, then M0131 top-to-bottom. Next
unchecked is **M0131-S2** (`LoadOrCreateSystemID` reads pg_control first,
`internal/initdb/initdb.go:54-76`); design doc `0131-0002` already drafted.
Note S10 is flagged LAND EARLY and is independent of S2.

Gates: `go build ./...` clean; `internal/config` + `internal/initdb` PASS;
`RALPH_PRECOMMIT_SCOPE=units scripts/ralph-precommit-test.sh` PASS (warm cache).
pgbench smoke via the commit hook. tpch-spotcheck NOT run — config-registration
only, no planner/executor/codec path touched.

In-flight: none
