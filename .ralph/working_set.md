Task just completed: M0134-0114 (create_role.sql) — sized live against PG 18.3
oracle via scripts/pg-regress-runner.sh: PARKED (diff 259→204 lines, still 0%
parity — single-test suite, not fully green).

Root cause: goopg's CREATE ROLE/ALTER ROLE/DROP ROLE run entirely through a
text-substitution intercept (internal/postmaster/role_ddl.go's
tryHandleRoleDDL, ahead of the parser, which has no role-DDL grammar) and had
ZERO privilege enforcement — at least 4 independent root-cause buckets.

Landed: ported PG's CreateRole/AlterRole permission gates
(postgres/src/backend/commands/user.c) into two new functions in
internal/postmaster/role_ddl.go:
- checkCreateRolePrivileges — non-superuser needs CREATEROLE to create any
  role, can never hand out SUPERUSER, can only hand out
  CREATEDB/REPLICATION/BYPASSRLS if it holds that attribute itself.
- checkAlterRoleAttrPrivileges — touching SUPERUSER always requires
  superuser; touching CREATEDB/REPLICATION/BYPASSRLS requires the actor hold
  it.
Wired via a new variadic `actingRole ...string` trailing param on
tryHandleRoleDDL, passed from the two real wire-dispatch call sites
(internal/postmaster/dispatch.go, dispatch_extended.go).

Design docs/design/m0134-0114-create-role-sizing.md, README.md indexed.
fix_plan.md M0134-0114 marked [x] PARKED. Ledger row appended
(.ralph/deferral_ledger.md, M0134-0114): remaining gaps —
(1) ADMIN-OPTION-on-target-role + object-ownership enforcement for
    ALTER/RENAME/DROP ROLE and DROP/ALTER...OWNER TO (goopg's
    operators_ddl_role_membership.go already has IsAdminOfRole but
    role_ddl.go doesn't consult it — likely the next-cheapest bucket).
(2) REASSIGN OWNED BY has zero parser support at all.
(3) createrole_self_grant GUC + automatic self-grant semantics unimplemented.
(4) SYSID backward-compat NOTICE missing (no notice channel in
    tryHandleRoleDDL's return shape).

NEXT LOOP: per the Current Priority banner in .ralph/fix_plan.md, continue
M0134 top-to-bottom — next unworked item is **M0134-0115
(create_schema.sql)**.

Standing recommendation (carried across several loops, still open — see prior
working_set snapshots / deferral ledger for full detail on each):
1. brin_summarize_range/brin_desummarize_range unimplemented (M0134-0095/-
   0096/-0097 PARKs).
2. Collation-execution-registry gap recurs across FIVE parked files
   (M0134-0099/-0100/-0101/-0102).
3. expr.go length/upper/lower/etc. swallow nested function-not-found errors
   into NULL instead of 42883 (M0134-0102 bucket 5, systemic).
4. ctid/tableoid system-column pattern (M0134-0104) generalizes to
   cmin/cmax/xmin/xmax.
5. LANGUAGE C dynamic-extension loading gap (M0134-0106) — no loader exists.
6. EUC_JP/UTF8 real Unicode mapping tables unported (M0134-0107).
7. `CREATE TABLE ... USING <am>` has zero parser support (M0134-0109).
8. `::` cast evaluator never consults pg_cast for user-defined casts
   (M0134-0110).
9. Operator-lexer whitelist gap (internal/parser/lexer.go:548-575, hardcoded
   2/3-char switch vs PG's general graphic-operator-char grammar) —
   M0134-0111, worth a dedicated milestone.
10. ALTER TABLE RENAME COLUMN inheritance recursion missing entirely
    (M0134-0112).
11. RAISE INFO/LOG/DEBUG collapse to hardcoded NOTICE wire severity
    (M0134-0113, ~18-call-site blast radius in plpgsql_runtime.go).
12. BETWEEN-vs-comparison-operator precedence bug (M0134-0113) — silently
    wrong for ANY query today, cross-cutting, candidate for a standalone
    bug-hunt loop.
13. NEW this loop: role-DDL privilege enforcement (M0134-0114) was FULLY
    ABSENT before this loop — the two landed checks cover attribute
    giveaway only; ADMIN-OPTION/ownership enforcement (item 1 above) is
    the natural next slice and likely recurs across other not-yet-sized
    M0134 role/privilege-adjacent cases (e.g. GRANT/REVOKE-heavy files).

Gates run (subagent-reported): go build ./... clean; go test
./internal/parser/... ./internal/executor/... ./internal/postmaster/... PASS;
RALPH_PRECOMMIT_SCOPE=units scripts/ralph-precommit-test.sh PASS; make
regen-testport clean; make check-testport-inventory PASS; make
ralph-state-guard PASS (auto-repaired a stale status/progress mismatch, then
confirmed consistent). Pre-commit hook's pgbench smoke ran automatically at
commit time — PASS. Commit 2d392628 pushed to origin/regress-renumbering.

In-flight: none.

Note: a concurrent peer session's WIP was present in the tree this loop
(.ralphrc, docs/wiki/*, ci/logs/*, analysis/*,
internal/executor/operators_recursive_cte.go, third-party/tpcds-postgres,
untracked `postgres` symlink) and was deliberately left untouched/
uncommitted — only the M0134-0114 files were staged and committed by
explicit pathspec.
