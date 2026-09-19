# Working Set — M0142-0003i (committing)

Task: **M0142-0003i** — canonical TPC-H FK set into `bench/tpch/build_schema_goopg.sh` (owner amendment: never DDL on `:65433`; script + private 55xx clone verify). `[x]` in fix_plan.

Files:
- `bench/tpch/build_schema_goopg.sh` — post-HammerDB block adds all 8 FKs (PG-`:65432`-identical, `DEFERRABLE` on `lineitem_order_fk`, composite `(l_partkey, l_suppkey)`), then `fk_check` fails the build on a partial landing.
- `docs/design/0100-0149/m0142-0003i-tpch-canonical-fk-set-in-build-script.md` — new, `Status: implemented`.
- `docs/design/README.md` — index row added after m0142-0003k.
- `.ralph/fix_plan.md` — `[x]` + DONE summary; stale BLOCKED tail rewritten as resolved.

Findings (private clone `:5533`, `pg_basebackup -X fetch` of `:65433`, now deleted):
- Restored `:65433` has all 8 PKs but **zero** FKs — script lands the full 8-FK set, not 5.
- All 8 validated index-accelerated, ~6m49s total (lineitem dominates, ~57µs/probe).
- `pg_constraint` PK/FK rows identical to PG `:65432`; only delta = PG `contype='n'` NOT-NULL rows (separate catalog gap).
- `pg_get_constraintdef` returns empty for ALL constraint types — cosmetic renderer gap, unrelated.
- **Q9 collapse resolved**: `lineitem ⋈ partsupp` est 117313 vs PG 75650 (was 2406); plan now walks FK-informed NL-index chain (Gather > NL+Memoize probes). Q9 = 175 rows both engines; value deltas = known HammerDB-vs-dbgen data divergence.
- Live `:65433` still has no FKs — owner applies at next reload per amendment.

Gates run: units gate PASS (44 ok, 0 FAIL). No Go code changed → tpch-spotcheck/SF0.25 not required; pgbench smoke runs in the commit hook. race-gate still red at HEAD = pre-existing `M-NIGHTLY-instrumentscope-race-fix` (not this loop's).

Next step: commit (script + doc + README + fix_plan + baton), run `make ralph-state-guard`, emit status.

In-flight: none.
