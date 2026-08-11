(idle — nothing in flight)

Last loop (#113): **M0131-S5 — runtime `pg_rewrite` index maintenance (2692/2693)**
— DONE, ticked, committed, pushed to `make-db-cluster-compat`.

M-NIGHTLY duty: `ci/logs/action-items.md` still run `20260811-014635` (12 items,
unchanged since loop #100). All already filed; the open ones stay PARKED per
banner. No new filing needed.

**The headline: a real PG 18.3 hosted on a goopg catalog now expands and reads
goopg-authored user views.** `SELECT count(*) FROM public.b5c_view` returned
42809 for the whole life of the M0123 line; it now returns the base-table count
on the promoted standby, and so do `b5c_view2` (bool/null WHERE) and
`b5c_view3` (searched CASE). The canonical `ev_action` serializer was never the
blocker — `writeViewRewriteRow` wrote the `_RETURN` heap row and DISCARDED the
TID, so `RelationBuildRuleLock` (relcache.c:785-806, `indexOK=true` hard
constant, no seq-scan fallback in genam.c:397-401) left `rd_rules` NULL and the
planner raised at plancat.c:139-147.

What landed:
- `writeViewRewriteRow` captures the TID → `insertPgRewriteRelRulenameIndexEntry`
  (2693, load-bearing) + `insertPgRewriteOidIndexEntry` (2692); both OIDs joined
  `mirroredCatalogOIDs()` (omitting the mirror = blocker #8 repeated).
- Gate: the soft `t.Logf` in `e2e_failover_goopg_to_pg_test.go` is now a hard
  count assertion over all three views, with a non-vacuity guard.
- New `internal/executor/sys_pg_rewrite_index_test.go` (2 tests) — leaf shape,
  TID resolving to the LIVE heap row with the right ev_class, mirror membership,
  and S5.5's DROP/recreate cycle VERIFIED (stale + fresh leaf, exactly one live).
- Three plan corrections: **S5.1 was already done** (`buildIndexTupleOidNameKey`
  / `cmpKeyOidName` already in `sys_pg_enum.go` for pg_enum 3503, identical
  80-byte layout — Guards 1/2 subsumed); `"_RETURN"` → `viewRuleName` const
  shared by heap row and index key; inline `mirroredOIDs` → `mirroredCatalogOIDs()`.
- Design `0131-0005` draft → accepted + §Findings; README row. 1 ledger row.

Next loop: per banner — M-NIGHTLY filing, then M0131 top-to-bottom. Next
unchecked is **M0131-S6** (flip `relhasrules=true` for the 6 nailed system
views; `relHasRules = true` is commented out at `internal/initdb/initdb.go:5817`
— RISKY, and S8a is marked "must precede S6 AND S7", so read S8a first and
consider taking it instead). Note S13.3 and S14.1 still share a root (trailing
nullable catalog attributes).

Gates: new component tests PASS; `TestE2E_FailoverGoopgToPG` PASS (11.5 s, both
subtests); UNITS PASS (no FAILs); pgbench smoke PASS via the commit hook. No
executor/planner/codec *query-path* change (catalog index maintenance only), so
tpch-spotcheck / TPC-DS SF0.5 not required.

In-flight: none
