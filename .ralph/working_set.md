(idle — nothing in flight)

## Loop #3 result — M0134-0172 landed

**Nightly triage:** `ci/logs/action-items.md` still run `20260828-235424`; both `## AI-`
items already filed (001 advisory-lock regression, 002 Q5 timeout). Nothing new to file.

**Baton check:** tree matched `(idle)` — zero modified `.go` files at start.

**Task:** M0134-0172 `stats_ext.sql` sized (**3754 diff lines / 435 `^+ERROR`**) →
**PARKED** at 3451 / 54. Residual is REFACTOR-tier: `CREATE STATISTICS` is parsed but
~unvalidated (~40 missing PG errors), `ALTER STATISTICS` is a no-op, and the planner
consumes **no** extended statistics at all (deps/ndistinct/MCV) — that's the bulk of
the 3451 lines and the case's whole point. CSV `not-tried` → `failed`.

**Shipped (engine-wide PL/pgSQL correctness fix):** `RETURN QUERY <query>` and the
static form of `FOR rec IN <query> LOOP` never called
`substitutePlpgsqlFrameVarsInSQL`, so **no** frame variable was visible to either —
not a local, not even a function parameter. `return query select v + n` raised 42703
on `v`. Two of the four SQL-string-capturing handlers substituted; these two didn't.
Also: NULL/out-of-range array subscripts now render `NULL` (were leaking bare
`tmp[1]` to the planner); routine OUT/`RETURNS TABLE` names excluded via new
`plpgsqlFrame.outParamNames`. Design
`docs/design/m0134-0172-plpgsql-query-stmt-frame-var-substitution.md`.

**Three things worth carrying:**

1. **A lazy convention is invisible at the site that forgets it.** `ReturnQueryStmt`
   looks perfect in isolation — it parses a string and plans it. Nothing there says a
   rewrite pass was owed; the obligation lives entirely in the *other* handlers. Exact
   mirror of -0171 (shared helper wrong, all call sites right). Neither auditing the
   helper nor the call sites suffices — enumerate the sibling set and prove it closed.
2. **The same shape recurred inside my own fix.** `substitutePlpgsqlFrameVarsInSQL`
   has THREE arms that consult the frame; I wired the new exclusion into two. Only
   subtest (b) caught it.
3. **A regress-runner diff delta can be pure cluster-state cascade.** `plpgsql` +10
   looked like a regression with 2 new `f1 is not unique` errors. Re-running the whole
   file against two **freshly-initdb'd** clusters showed *exactly one* changed
   statement — the fix. The runner's shared 14-case cluster had manufactured the rest.
   Diff-the-diffs lies; diff goopg's own output on clean dirs.

Gates run: `go build ./...` OK; guard `TestPlpgSQLQueryStmtsSubstituteFrameVars` PASS,
revert-checked (6 of 7 subtests fail on old body; 7th is the over-reach guard pinning
that `FOR IN EXECUTE` is still NOT substituted); 14-case regress A/B (13 byte-identical,
`plpgsql` traced to statement level = no regression); `plpgsql.sql` A/A'd first (noise
±1/side); `RALPH_PRECOMMIT_SCOPE=units scripts/ralph-precommit-test.sh` PASS;
`scripts/tpch-spotcheck.sh` PASS (Q12 rows=2, Q13 rows=34); `make regen-testport` +
`make check-testport-inventory` PASS; `make ralph-state-guard` OK after self-repair.

In-flight: none.

**Carried obligations (17th loop):** TPC-DS SF0.5 gate still NOT run (for -0156,
-0157). -0158..-0172 are parser/DDL/catalog/ACL/wire/type-input/FK/plpgsql-only and
cannot move a TPC-DS plan.

**Env notes:** foreign orphan goopg holds **port 5533** (pid 1047197, not ours — do not
kill); probe on 5540+. Probe dirs `/tmp/probe5541`, `/tmp/pl-fix`, `/tmp/pl-base` and
binaries `/tmp/goopg-{probe,fix,base}` are throwaway; all servers stopped.

**NEXT LOOP:** banner rules — M-NIGHTLY filing, then **M0134-0173 (`stats_import.sql`,
status `not-tried`)**.
