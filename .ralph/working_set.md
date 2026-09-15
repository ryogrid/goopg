Task: M0142-0003e — determine why goopg's existing superkey/FK no-fan-out
mechanism doesn't fire for the bare `lineitem ⋈ partsupp` composite join.
**DONE and committed this loop.** Measurement-only (two read-only `psql`
queries against the shared `:65433` bench cluster and `:65432` PG oracle;
`go test` runs against existing unit tests; no production code changed, no
server started/stopped/restarted).

Files: `.ralph/fix_plan.md` (M0142-0003e rewritten `[x]` with the finding;
new M0142-0003f filed `[ ]`). `.ralph/deferral_ledger.md` (new m0142-0003e
row). `docs/design/0100-0149/m0142-0003e-bench-cluster-missing-fk-constraints.md`
(new). `docs/design/README.md` (indexed). No `internal/` files touched.

Key symbols (read, not edited): `internal/optimizer/joinkeyproof.go`'s
`provableJoinKeys` (the `!k.fromFK` bound-only loop vs the `k.fromFK`
selectivity-firing loop) and `superkeyJoinEstimate`;
`internal/optimizer/joinrelsize.go`'s `superkeyJoinSelectivity`;
`internal/catalog/catalog.go`'s `ForeignKey` struct (fully implemented, not
a stub); `internal/parser/ast.go:3389` (purpose-built HammerDB-TPC-H FK-shape
support already exists in the parser).

Findings this loop (settles -0003e, no code change indicated): (1)
`GOOPG_PGSHAPED_DP` re-confirmed ON by default (`joinsearch.go:76`) — the
FK/superkey arm is live in production. (2) The exact repro is ALREADY covered
byte-for-byte by existing tests in `internal/optimizer/joinrelsize_test.go`
(`TestCalcJoinrelSizeCompositeUniqueRetainsEqualities`,
`TestCalcJoinrelSizeBareCompositeDefaultsKeepEqualityAndBound`,
`TestCalcJoinrelSizeFKDividesByParentCount` — all 4 relevant tests PASS at
HEAD) — same 6,000,000/800,000 row counts, same NDistinct 200000/10000, same
composite UNIQUE index. They prove `superkeyJoinSelectivity` DOES recognize
`partsupp_pk` against this clause shape but DELIBERATELY treats a bare
non-FK UNIQUE index as bound-only evidence (never fires the `1/rawTuples`
selectivity substitution) — matching PG's own `get_foreign_key_join_selectivity`
(selfuncs.c), which also requires a declared `pg_constraint` FK row, not
just a unique index on the referenced side. (3) **The decisive check**: read
-only `pg_constraint`/`\d`/`pg_indexes` queries against BOTH live clusters
showed goopg's `:65433` TPC-H bench cluster has **zero** FK constraints on
any table (`lineitem_part_supp_fkidx` is just a plain index despite its
name), while the `:65432` PG 18.3 oracle has the **full canonical 8-row TPC-H
FK set** (`lineitem_partsupp_fk`, `lineitem_order_fk`, `partsupp_part_fk`,
`partsupp_supplier_fk`, `order_customer_fk`, `supplier_nation_fk`,
`customer_nation_fk`, `nation_region_fk`), added by an untracked manual step
outside any script in the repo, never mirrored onto goopg's cluster. PG's own
`EXPLAIN` on the bare join gets `rows=5999098` (essentially exact) via that
declared FK + `Memoize`+`Index Scan using lineitem_part_supp_fkidx`.
`grep` across `bench/tpch/*.sh`/`bench/tpch/tcl/build_schema.tcl` for
`FOREIGN KEY`/`ADD CONSTRAINT`: zero matches — HammerDB's own schema builder
declares none of these on EITHER side. **Conclusion: this is a bench-cluster
schema/data-load parity gap, not a planner or cost-model defect.** goopg's FK
machinery is fully implemented and ready (not the blocker). This also
reframes -0003c's "real 64% PG-side cost gap": that PG measurement carried
PG's own FK-informed near-exact row estimate throughout, while the
goopg-side shape being priced carried the 2500x-collapsed one — the two
costs were never pricing comparably-sized intermediates, so -0003c's 64%
figure cannot yet be trusted as a pure cost-formula discrepancy.

Next step: **M0142-0003f** (filed this loop) — add the missing 8 FK
constraints to goopg's `:65433` TPC-H bench cluster (exact names/columns
captured this loop from the PG oracle's `pg_constraint`) via `ALTER TABLE
... ADD CONSTRAINT ... FOREIGN KEY ...` (parser support already exists,
`ast.go:3389`/`ddl.go:10276` — untested at runtime this loop, worth a small
smoke check first). Land the DDL in `bench/tpch/build_schema_goopg.sh` (or a
new post-load step) so it survives a `--reset` rebuild rather than being a
one-off manual `ALTER TABLE` against the live cluster, then re-run the Q9
`EXPLAIN`/estimate-audit capture from -0003a/-0003d to see whether the
collapse is gone and whether Q9's plan shape or cost gap actually changes —
a fresh measurement, not assumed. Also worth a quick corpus-wide
`pg_constraint` diff between the two clusters before trusting any OTHER
M0142 "row-estimate collapse" finding's causal story (this same gap could be
masquerading as an estimator bug elsewhere in M0142-0004a/0004b/0009/0010
etc.). Other still-open M0142/M0141 items, none mandated over 0003f by the
banner (still item 4): **M0142-0005** (per-worker Memoize cache, large,
needs its own scoping recon), **M0142-0008a/0008b** (SEMI/ANTI decorrelation
scoping), **M0142-0016c** (Q33/Q54/Q56 shape check), **M0141-S2b/S3-S7**
(upper-planner ordering, Incremental Sort).

Gates run: `git status --porcelain -- internal/` empty before AND after this
loop's work (no production code touched). `go test ./internal/optimizer/...
-run '<the 4 relevant tests>'`: all PASS. `make ralph-state-guard`: same
pre-existing stale progress-marker inconsistency as the last several loops
(status=running vs a stale progress=completed marker from a prior loop's
clean exit), self-repaired to in_progress, then passed clean. Pre-commit
pgbench smoke gate: ran at commit time (see commit for PASS/FAIL). Practice
-card row-count gate suite not required (no production code touched, same
reasoning as -0003c/-0003d/-0005/-0008).

In-flight: none. No server started or stopped this loop. Two read-only
`psql` queries were run against the shared `:65433` bench cluster (`\d
lineitem`, `pg_constraint`/`pg_indexes` SELECTs) and two against the
`:65432` PG oracle (`pg_constraint` SELECT, one `EXPLAIN`) — no writes, no
restarts, no data changed on either cluster. Nightly CI batch
(`ci/logs/action-items.md`) mtime unchanged since the last two loops'
triage (2026-09-16 04:45) — no new run landed, so triage was correctly
skipped again this loop per the working-set instruction; the next loop
should re-check mtime before assuming it's still current.
