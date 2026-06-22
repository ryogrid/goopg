Task: M0118-0009 `subxid-overflow` — DONE this loop. Spec PROMOTED `failed`→`pass`.
Design 0118-0021. Committing.

WHAT LANDED (PL/pgSQL front-end only — NO MVCC/storage change):
- The spec's recursive `gen_subxids(n)` function opens 100 nested subxids via
  per-frame EXCEPTION handlers (overflows subxid cache) while other sessions probe
  XidInMVCCSnapshot / XactLockTableWait. goopg's subxact visibility+lock machinery
  was ALREADY correct — the function just wouldn't COMPILE. Two parser gaps:
  1. Bare `RETURN;` rejected at parse (parseReturn always demanded an expr).
     FIX: parseReturn accepts immediate `;` → ReturnStmt{Expr:nil}. Runtime
     (plpgsql_runtime.go ReturnStmt arm) short-circuits nil Expr: trigger→
     flowReturnTriggerNull; VOID fn→NullDatum/flowReturn; value fn→ExecError
     42601 "missing expression" (mirrors upstream make_return_stmt).
  2. `NULL;` no-op stmt (empty EXCEPTION handler body) → "unsupported PL/pgSQL
     statement". FIX: new NullStmt AST node (ast.go), parseStmt case for KwNull
     (parser.go), no-op runtime arm (plpgsql_runtime.go).
- Files: internal/plpgsql/{parser.go,ast.go,parser_test.go},
  internal/executor/plpgsql_runtime.go, internal/testport/isolation_port_test.go
  (new TestPort_IsolationSubxidOverflow), docs/design/0118-0021-*.md + README,
  docs/test-port inventory row 603 failed→pass + regen'd coverage/inventory md,
  .ralph/fix_plan.md.
- Replaced obsolete TestParseRejectsBareReturn with TestParseAcceptsBareReturn;
  added TestParseNullStatement, TestParseExceptionHandlerNullBody.

Gates (green): TestPort_IsolationSubxidOverflow 4/4 PASS; plpgsql -race;
executor plpgsql unit; isolation regression batch (FreezeTheDead, InplaceInval,
MultixactNoForget, AbortedKeyrevoke, EvalPlanQualTrigger, DeleteAbortSavept) PASS;
gofmt flags on ast.go/plpgsql_runtime.go are PRE-EXISTING go1.25-vs-1.26 churn (NOT
my lines — never gofmt -w); pgbench smoke via pre-commit hook at commit.

NEXT loop candidates (remaining M0118-0009 misc, all need NEW subsystems — probed
this loop, ranked by cost): intra-grant-inplace (ALTER TABLE ADD PK must `<waiting>`
on a GRANT's pg_class catalog-tuple xmax lock — needs catalog row lock; ~9 lines from
match but semantic gap large); horizons ($$-dollar-quoted EXPLAIN bodies choke the
isolation lexer + EXPLAIN FORMAT json + json `->` ops + explain_json()); temp-schema-
cleanup (pg_my_temp_schema() + temp object cleanup on exit/DISCARD + advisory locks);
stats (pg_stat_force_next_flush + pg_stat_* infra); async-notify (LISTEN/NOTIFY
unimplemented in parser/executor — large); prepared-transactions (2PC). OR a different
M0118 group (0118-0005 FK, 0118-0006 MERGE, 0118-0008 DDL/VACUUM).

GOTCHAS: never gofmt -w (go1.25 repo vs local 1.26). Isolation specs run goopg as a
SUBPROCESS. CSV rationale comma-free. tpch-spotcheck INFRA-BLOCKED; pgbench smoke is
the live guard. Untracked postgres/ + weekly_loc.* + requirements.txt are stray — leave.
