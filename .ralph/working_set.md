(idle — nothing in flight)

## Loop #4 result — M0134-0173 landed

**Nightly triage:** `ci/logs/action-items.md` still at run `20260828-235424`; both
`## AI-` items already filed (001 advisory-lock FIXED, 002 Q5 timeout open). Nothing new.

**Baton check:** tree matched `(idle)` — zero modified `.go` files at start.

**Task:** M0134-0173 `stats_import.sql` sized live for the first time
(`not-tried` → **`failed`**, 1461 diff lines / 74 `^+ERROR`) → **PARKED** at 1457 / 73.
Residual is ~100% the PG 18 statistics-IMPORT function family
(`pg_restore_relation_stats` / `pg_restore_attribute_stats` / `pg_clear_*_stats`,
55 of 73 errors) plus no queryable `pg_statistic` (5 more). Ledger 0173c/0173d.

**Shipped (engine-wide silent wrong-answer fix):** goopg treated every range-typed
value as **opaque, unvalidated text**. `evalCast` had NO arm for any range type, so
`'garbage'::int4range` succeeded, `'[5,1)'::int4range` succeeded where PG raises
22000, and — the serious half — **no discrete range was ever canonicalized**, so
`'[1,4]'::int4range` and `'[1,5)'::int4range` (the SAME value in PG) compared
UNEQUAL through every equality, ORDER BY, btree probe and exclusion constraint.
Second half: the six built-in range constructors were 42883 despite all twelve
`range_constructor2/3` rows sitting in goopg's `pg_proc` seed. New
`internal/executor/rangetypes.go`; design
`docs/design/m0134-0173-range-type-input-and-constructors.md`.

**Three things worth carrying:**

1. **"Catalog advertises what the executor never implemented" is now a named
   recurring shape** — M0134-0167 (IndexAMCapability) and this one within a week.
   When a `pg_proc`/`pg_am` seed row exists, that is NOT evidence the feature runs.
   Probe the SQL, not the catalog.
2. **`evalCast(d, "text")` is NOT a general output function.** Its arm converts only
   the kinds needing a session GUC (KindTime, KindBytes) and returns everything else
   unchanged — a `KindNumeric` comes back as itself, so `StringValue()` is `""`.
   `numrange(1.0,4.0)` first rendered `["","")`. Only the live oracle A/B caught it;
   reading the arm did not.
3. **A `git worktree add` of this repo does NOT get `postgres/`.** It creates an
   EMPTY submodule dir (containing only the inner `postgres -> ../postgres` link),
   so the regress runner silently finds no SQL. `rm` the inner link, `rmdir`, then
   `ln -s /home/ryo/work/goopg/goopg/postgres <worktree>/postgres`.

Gates run: `go build ./...` OK; guard `TestRangeTypeInputAndConstructors` (8 subtests)
PASS, **revert-checked at BOTH wiring points** (deleting the `evalCast` arm fails the
cast subtest; deleting the `evalFuncCall` case fails the dispatch subtest); 43-statement
oracle A/B vs a throwaway PG 18.3 — every value/message/DETAIL/HINT matches; 14-case
regress A/B vs a HEAD worktree (`rangetypes` 2543→2166 / 234→182 `^+ERROR`,
`multirangetypes` 4252→4235, 9 byte-identical, `create_index` delta = pre-existing
nondeterministic pointer-address leak in `pg_get_indexdef`, `plpgsql` +5 traced line by
line to the polymorphic `f1(anyrange,…)` block = no regression);
`RALPH_PRECOMMIT_SCOPE=units scripts/ralph-precommit-test.sh` PASS;
`scripts/tpch-spotcheck.sh` PASS (Q12 rows=2 24.0s, Q13 rows=34 8.5s);
`make regen-testport` + `make check-testport-inventory` PASS;
`make ralph-state-guard` OK after self-repair.

In-flight: none.

**Carried obligations (18th loop):** TPC-DS SF0.5 gate still NOT run (for -0156, -0157).
-0158..-0173 are parser/DDL/catalog/ACL/wire/type-input/FK/plpgsql-only and cannot move
a TPC-DS plan.

**Env notes:** foreign orphan goopg still holds **port 5533** (pid 1047197, not ours —
do not kill); probe on 5540+. Throwaway dirs `/tmp/probe0173`, `/tmp/pgoracle0173`,
worktree `/tmp/gp0173head` — all servers stopped and the worktree removed.

**NEXT LOOP:** banner rules — M-NIGHTLY filing, then
**M0134-0174 (`subscription.sql`, status `not-tried`)**.
