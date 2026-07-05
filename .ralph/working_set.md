(idle — nothing in flight)

Loop #8 landed and committed the DU-002 dump+restore round-trip probe at
commit `97584262` (pushed to origin/align-data-structure-with-pg).

Task: M0110-0001 (pg_dump TAP, DU-002 slice-by-slice advancement via the
self-promoting `TestPort_PgDumpConnectionSetup` guard). E-002's stated close
condition for 002-010 is "a dump+restore round-trip against a live goopg
server", but the guard test only ever checked that `pg_dump` itself exits 0
— it never actually restored the dump anywhere. Added that missing step.

Files: `internal/testport/pgdump_connsetup_test.go` (round-trip probe added
at the tail of the `res.ExitCode == 0` block, right before its `return`);
`internal/config/defaults.go` + `internal/config/postgresql.conf.sample`
(new `xmloption` GUC registration); `.ralph/deferral_ledger.md` (new
2026-07-06 row), `.ralph/fix_plan.md` (M0110-0001 item updated),
`docs/design/0110-0001-pg-dump-tap-port.md` (new "DU-002 round-trip probe"
section) + `docs/design/README.md` (one-line addendum on the existing huge
row).

Key symbols: `TestPort_PgDumpConnectionSetup` (the round-trip step: `CREATE
DATABASE dumprestore_du002` then pipe `pg_dump`'s stdout into `psql -v
ON_ERROR_STOP=1` against it); `catalog.InMemory.CreateDatabase`
(`internal/catalog/catalog.go:4231` — the root cause below).

Findings:
1. FIXED: every `pg_dump` archive opens with an unconditional `SET xmloption
   = content;` preamble; goopg didn't recognize the GUC. Registered it
   (enum content/document, default content, PGC_USERSET) — no-op beyond
   accepting SET/SHOW, since goopg's XML codec has no document-mode parsing.
2. FOUND, NOT FIXED (milestone-scale, do not attempt in one loop): the
   round-trip immediately then hits `ERROR: collation "builtin_coll" already
   exists`. Confirmed via a throwaway probe (deleted after use) that
   `catalog.InMemory` has NO per-database namespace at all — `CreateDatabase`
   only sets a boolean in `c.databases[name]`; every real object store
   (`c.tables`, `c.schemas`, `c.userCollations`, etc.) is one flat
   server-wide map with no DBOid/DBName key. A table created in "postgres"
   is visible from, and collides with, any other "database" name on the
   same server. This blocks ANY dump-into-fresh-database round trip, not
   just this one collation. Full writeup + resume point in the
   2026-07-06 deferral-ledger row and the design doc's new section.

Next step: pick the next M0119-0004/M0110-000x item — do NOT attempt real
per-database catalog isolation as a single loop (it's milestone-scale: every
`catalog.InMemory` object map needs a DBOid key threaded through create/
lookup/drop/rename, plus auditing whether `storage.RelFileNode.DBOid` /
`InMemory.dbOid` is ever actually multi-tenant). The round-trip probe stays
a soft `t.Logf` (not a hard assertion) so it's a ready-made regression
signal once/if that milestone lands. Otherwise continue advancing DU-002 via
further catalog-view-parity slices, or pick up the M0122-0003 `reuses`
pg_stat_io counter / 4 writeback simplifications (both larger/fuzzier, per
loop #7's note), or another M0119-0004 ledger row.

Gates run: `go build ./...` clean; `go test -count=1
./internal/config/... ./internal/parser/... ./internal/catalog/...
./internal/server/... ./internal/executor/...` all PASS; full
`scripts/ralph-precommit-test.sh` (unit suite minus cluster-backed packages
+ pgbench smoke) PASS; `scripts/tpch-spotcheck.sh` PASS (Q12=2/Q13=33);
`make ralph-state-guard` OK (auto-repaired the usual stale completed-marker
pattern, same as every recent loop).

Note: an untracked `postgres` directory/submodule shows build-artifact
content (GNUmakefile, config.log, etc.) — pre-existing, not touched or
committed this loop.
