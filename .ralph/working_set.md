Task: M0118-0009 `inplace-inval` — DONE this loop. Spec PROMOTED `failed`→`pass`
with NO code change (passes by construction). Design 0118-0019. COMMITTING.

WHAT LANDED (test + docs only, zero engine change):
- NEW dedicated test `TestPort_IsolationInplaceInval`
  (internal/testport/isolation_port_test.go) — sequential, fresh cluster, both
  permutations byte-match PG 18.3 expected. PASS in 1.43s.
- Inventory CSV row (postgres-oracle-target-inventory.csv L534) `failed`→`pass`,
  comma-free rationale naming the Go test func.
- Regenerated upstream-isolation-coverage.md + postgres-oracle-target-inventory.md.
- Design 0118-0019 + README.md index entry.

WHY IT PASSES (root cause): goopg serves `pg_class` from the VIRTUAL catalog
builder and recomputes `relhasindex` live on every read as
`len(c.byTable[t.OID])>0` (internal/catalog/catalog.go ~L2599). The upstream bug
(an `heap_inplace_update`-set relhasindex reverted by a later `heap_update` using
a stale catcache oldtup) needs a heap pg_class tuple + catcache + inplace path —
ALL absent in goopg. So result = "does an index currently exist": cir1 rolls
back (no index), cic2 commits i2 (index exists), ddl3 ALTER ADD COLUMN doesn't
touch indexes → read1 = relhasindex=t in both perms. Caveat: immunity holds only
while pg_class stays virtual/derived.

Gates (green): TestPort_IsolationInplaceInval PASS; go build ./...; go vet
./internal/testport/; gofmt clean for my edits. No -race/pgbench needed (no
engine change) but pre-commit hook will run pgbench smoke at commit.

NEXT loop: remaining M0118-0009 misc specs all need substantial NEW subsystems
(verified via diag run this loop):
- async-notify → full LISTEN/NOTIFY + pg_notify + async queue (not implemented).
- temp-schema-cleanup → pg_my_temp_schema + temp objects + plpgsql.
- horizons → spec uses $$-dollar-quoted EXPLAIN bodies (isolation lexer chokes:
  "lex error unterminated dollar-quoted string") + EXPLAIN(FORMAT json,BUFFERS).
- freeze-the-dead → VACUUM FREEZE multixact visibility (CLOSE: one row diff).
- intra-grant-inplace → ALTER TABLE ADD PK should `<waiting...>` on a pg_class
  FOR KEY SHARE row lock; goopg doesn't block (CLOSE-ish, needs catalog row lock).
- intra-grant-inplace-db → needs pg_database.datfrozenxid column (M0117-0008).
- prepared-transactions{,-cic} → PREPARE TRANSACTION / 2PC (parser+engine).
- subxid-overflow → plpgsql `RETURN;` (empty expr) parse error + EXCEPTION subxids.
Best next candidates by closeness: freeze-the-dead (1-row diff) or
intra-grant-inplace (catalog-row-lock wait). OR a different M0118 group
(0118-0005 FK, 0118-0006 MERGE, 0118-0008 DDL/VACUUM).

GOTCHAS: never gofmt -w (go1.25 repo vs local 1.26). Isolation specs run goopg as
a SUBPROCESS. CSV rationale comma-free. cd /home/ryo/work/goopg/goopg first.
tpch-spotcheck INFRA-BLOCKED; pgbench smoke is the live guard. Untracked postgres/
+ weekly_loc.* + requirements.txt are stray artifacts — leave them.
