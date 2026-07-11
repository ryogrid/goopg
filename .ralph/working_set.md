(idle — nothing in flight)

## Loop summary (2026-07-12, loop #78)

**Task (M0122-0003 sub-slice):** added the `pg_statio_all/user/sys_sequences`
trio (5 cols `relid/schemaname/relname/blks_read/blks_hit`, relkind 'S'),
completing the `pg_statio_*` family after the tables/indexes I/O views (loop #77).

- `catalog.PGStatioSequencesRowsForDBOid(dbOid, scope)`
  (internal/catalog/catalog.go): the ONLY pg_statio builder whose relation
  filter SELECTS sequences (`t.IsSequence`) instead of skipping them; same
  `StatTableScope` user/sys split; identity cells real, both block counters a
  faithful 0 (no per-relation buffer attribution).
- Registered 3 virtual views (OIDs 9093–9095) in `registerSystemTables`.
- `executor.fetchStatioSequencesRows` (internal/executor/pgstat_tables.go) is
  the per-connection twin; wired at valuesOp.Open (internal/executor/operators.go)
  via 3 new branches.
- Tests: internal/catalog/pgstatio_test.go
  (TestPGStatioSequencesRowsBasicShape / ScopeFilter) +
  internal/executor/pgstatio_e2e_test.go (TestPgStatioUserSequencesEndToEnd) — PASS.
- Design: docs/design/0122-0003-pg-stat-user-tables.md new "Sequence sibling"
  section + README row extended. Ledger row appended.

**Gates:** catalog+executor full packages PASS; go build ./... clean;
tpch-spotcheck PASS (Q12=2/Q13=33); pgbench smoke via pre-commit hook.
Committed + pushed (pending this loop).

**Nightly:** action-items.md run 20260712-020530 already fully triaged by loop
#77 (39 testport items = 121-connection-refused co-load cascade, confirmed
non-regression). No new M-NIGHTLY task.

**Next natural slice:** the deferred per-relation buffer-pool block attribution
(a `BufferUsage`-per-relation analog at `storage.Pool.Pin()`) would let the
whole `pg_statio_*` family report real hit/read counters instead of 0. Larger,
cross-cutting storage-engine slice. See ledger.

In-flight: none
