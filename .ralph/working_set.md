Task: M0127-P5.6-f-pre — DONE and committed. Selected M0127-P5.6-f per the
banner, found a hard blocker in front of it, fixed the blocker.

**The finding (do not re-derive).** goopg's TPC-H bench cluster (db `tpch`,
65433) carries **0 user indexes and 0 constraints**; the PG 18.3 reference
(65432, same schema) carries **16 and 8**. HammerDB DID create them (names are
still in that data dir's pg_wal) — they were lost at the first restart. Root
cause is a composed regression: 4e follow-up 39 deferred index catalog rows
because `RecordKindCreateIndex(20)`/`DropIndex(21)` still carried them, then B5
Slice A retired 20/21 on the premise `loadUserIndexesFromHeap` replaced them —
but the write went to `base/<DefaultDBOid>` and that reload scans only
`cat.DBOID()`, resolving the owning table in the wrong namespace. So on ANY
non-default database, CREATE INDEX / PRIMARY KEY / ADD CONSTRAINT was durable
nowhere.

Files: `internal/executor/operators_ddl.go` (write routing +
BOTH DROP INDEX stamp sites), `internal/initdb/open.go`
(`loadUserIndexesFromHeapForDB` + per-DB sweep),
`internal/catalog/catalog.go` (`RegisterIndexDuringRecoveryForDB` +
`Index.DBOid`), `internal/server/index_dbid_restart_test.go` (new),
`docs/design/0122-0018-per-database-catalog-namespace.md` + README row.

Key symbols: `tableCatalogHeapDBOid` / `tableCatalogDBOids` (the routing pair
follow-up 39 introduced for tables — index rows now use both),
`loadUserIndexesFromHeapForDB(heapDBOid, nsDBOid)`, `IndexRelFileNode`.

Gotcha worth keeping: fixing only the ROUTING made every metadata assertion
pass while the index silently stopped ENFORCING — `IndexRelFileNode` read the
process-wide `InMemory.dbOid` because recovery never stamped `Index.DBOid`, so
a reloaded UNIQUE index accepted duplicates. Only the duplicate-insert
assertion caught it. Any similar "reloaded catalog object" work should assert
behaviour, not just catalog visibility.

**NEXT LOOP: re-read the `## Current Priority` banner (it wins over this
note). It should still select `M0127-P5.6-f`, which now carries a STEP 0.**
Step 0 is mandatory and is written out in the fix_plan item: the fix that
landed is FORWARD-ONLY (the bench cluster's old index rows sit in a heap the
new per-DB sweep does not read for db `tpch`), so `partsupp_pk` must be
re-created before `fkselec` has anything to fire on:
`CREATE UNIQUE INDEX partsupp_pk ON public.partsupp USING btree (ps_partkey,
ps_suppkey)`. Landing the multi-key half WITHOUT step 0 is a guaranteed
regression — it takes Q9 from 80× over to ≈2.5 M× under (≈2 rows). Creating
the index MOVES the estimate-audit baseline (every audit through
`2026-08-04-p56eiii` was index-free) — re-baseline in the same loop and say so.

Carried over, still true: Q9's bad joinrel is `lineitem ⋈ partsupp` on
`(l_suppkey=ps_suppkey AND l_partkey=ps_partkey)`, EXPLAIN shows
`rows=480067320` where truth is `5 997 241`; `estimateJoin` (cardinality.go
:448-465) prices ONE pair while `joinResidualSelectivity` excludes BOTH.
`joinrelsize.go`'s `superkeyJoinSelectivity`/`keysCovering` is the already
reviewed implementation of the fix — but it lives on the PG-shaped DP path
(`GOOPG_PGSHAPED_DP` OFF), so P5.6-f has to bring the same two halves into the
legacy `estimateJoin`, which has NO catalog in scope (see plan.go:589-593 —
`EstRelRows`/`SmallDim` are the stamp-at-plan-build precedent to follow).
PG's own Q9 plan filters `part` first and index-scans lineitem via
`lineitem_part_supp_fkidx`.

Also still open: `pg_constraint` per-DB routing (the (b) half of this loop's
ledger row) — goopg's `tpch` cannot get the 8 FKs back until that lands.

Gates run this loop: build + vet clean; `go test` on catalog/initdb/executor/
server PASS; UNITS PASS (exit 0, `/tmp/units_p56fpre.log`); SPOT PASS (Q12=2,
Q13=35, `/tmp/spot_p56fpre.log`); DS05 sweep (`/tmp/ds05_p56fpre.log`);
pgbench smoke via the commit hook.

Nightly triage: same 17 `AI-20260804-005028-*` subjects, all already filed
under M-NIGHTLY. Nothing new to file.

In-flight: none.
