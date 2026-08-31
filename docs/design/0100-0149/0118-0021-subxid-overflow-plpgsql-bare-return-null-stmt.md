# 0118-0021 — `subxid-overflow` isolation spec: PL/pgSQL bare `RETURN;` + `NULL;` no-op statement

**Milestone:** M0118-0009 (Upstream Isolation Spec Suite Pass-Through — Misc / system-level specs)
**Status:** Landed — `subxid-overflow.spec` promoted `failed` → `pass`.
**Spec:** `postgres/src/test/isolation/specs/subxid-overflow.spec`
**Test:** `TestPort_IsolationSubxidOverflow` (`internal/testport/isolation_port_test.go`)

## 1. What the spec exercises

`subxid-overflow` is designed to drive the code paths that only fire once a
single transaction has **overflowed its subtransaction cache** (PG keeps the
most-recent 64 subxids per backend in `PGPROC`; beyond that the snapshot is
marked *overflowed* and visibility/lock probes must fall back to `pg_subtrans`
rather than the in-proc cache).

The overflow is manufactured with a recursive PL/pgSQL function:

```sql
CREATE OR REPLACE FUNCTION gen_subxids (n integer) RETURNS VOID LANGUAGE plpgsql AS $$
BEGIN
  IF n <= 0 THEN
    UPDATE subxids SET val = 1 WHERE subx = 0;
    RETURN;
  ELSE
    PERFORM gen_subxids(n - 1);
    RETURN;
  END IF;
EXCEPTION /* generates a subxid */
  WHEN raise_exception THEN NULL;
END;
$$;
```

Each recursive frame has an `EXCEPTION` block, and **a PL/pgSQL block with an
exception handler runs inside its own subtransaction** — so `SELECT
gen_subxids(100)` opens 100 nested subxids in session `s1`, overflowing the
cache. The other sessions then probe:

- **test1** — `s2sel` reads the row while `s1`'s overflowed subxid is still
  running → exercises `XidInMVCCSnapshot()` on the overflow path (xid found).
- **test2** — RR/RC snapshots look for data written by a *committed* sibling
  subxact `sub3` while `s1` is still overflowed → `XidInMVCCSnapshot()` xid-not-found.
- **test3** — `s2upd` blocks on the row `s1` is updating → `XactLockTableWait()`
  for an overflowed parent.

## 2. Root cause — two PL/pgSQL parser gaps, no MVCC bug

goopg's subtransaction visibility and lock machinery (pg_subtrans-backed
`TopLevelXid` / `IsAborted` / `xidActiveWithSubxact`, the cumulative multixact
producer, savepoint-scoped row locks — all landed across M0118-0003/0004/0009)
were **already correct** for the overflow case. The spec failed purely because
goopg's PL/pgSQL parser could not compile `gen_subxids`:

1. **Bare `RETURN;` rejected at parse time.** `parseReturn` always required an
   expression (`scanExprToSemicolon("RETURN expression")`), erroring with
   "RETURN expression requires a non-empty expression". A previous
   Stage-A test (`TestParseRejectsBareReturn`) even documented this as a known
   limitation: "`RETURN;` … is upstream-legal for void / OUT-only routines."

2. **`NULL;` no-op statement unsupported.** The `EXCEPTION` handler body is the
   PL/pgSQL no-op statement `NULL;`. `parseStmt` had no case for the `NULL`
   keyword, so it fell through to "unsupported PL/pgSQL statement".

## 3. Fix

Purely PL/pgSQL front-end work; no executor/MVCC/storage change.

### 3a. Bare `RETURN;` (`internal/plpgsql/parser.go`, `internal/executor/plpgsql_runtime.go`)

- **Parse:** `parseReturn` now accepts an immediate `;` after `RETURN`,
  producing `ReturnStmt{Expr: nil}`. The parser does not know the function's
  return type, so the void-vs-value distinction is deferred to runtime (this
  mirrors upstream `pl_gram.y`, where `RETURN` is syntactically accepted and the
  return-type check happens in `make_return_stmt`).
- **Execute:** the `*plpgsql.ReturnStmt` arm short-circuits when `Expr == nil`:
  - trigger context → `flowReturnTriggerNull` (RETURN NULL);
  - VOID-returning function → exit returning `NullDatum` (`flowReturn`);
  - **value-returning function → error `42601 "missing expression"`**, matching
    upstream's behavior (a bare `RETURN` in a value function reaches
    `read_sql_expression` with nothing before `;` → "missing expression").

### 3b. `NULL;` no-op statement (new `NullStmt` AST node)

- `internal/plpgsql/ast.go`: new `NullStmt` (position only).
- `internal/plpgsql/parser.go`: `parseStmt` matches the `NULL` keyword and
  parses `NULL ;` into a `NullStmt`.
- `internal/executor/plpgsql_runtime.go`: the `*plpgsql.NullStmt` arm is a pure
  no-op (`flowNone`).

## 4. Oracle correspondence

- `postgres/src/pl/plpgsql/src/pl_gram.y` — `make_return_stmt` (bare-`RETURN`
  return-type rules: VOID/procedure accept, value-function → "missing
  expression"); the `NULL;` statement is `stmt_null` in the same grammar.
- Overflow semantics (`XidInMVCCSnapshot`, `XactLockTableWait`,
  `SubTransGetTopmostTransaction`) are unchanged on the goopg side — they were
  already faithful; this spec only needed the function to compile.

## 5. Tests / gates

- **Spec:** `TestPort_IsolationSubxidOverflow` — all 4 permutations byte-identical to PG 18.3.
- **Unit (parser):** `TestParseAcceptsBareReturn` (replaces the obsolete
  `TestParseRejectsBareReturn`), `TestParseNullStatement`,
  `TestParseExceptionHandlerNullBody`.
- **Regression:** `go test ./internal/plpgsql/...`; `-race` on
  `internal/executor` + the isolation row-lock/savepoint/multixact batch;
  pgbench smoke at commit (PL/pgSQL is on the function-call path).

## 6. Scope / non-goals

This lands the two front-end gaps the spec needed. Bare `RETURN`/`NULL;` are
now generally available to every PL/pgSQL function, not just the spec's. No
change to overflow detection, snapshot building, or `pg_subtrans` — goopg
already overflows correctly via per-EXCEPTION-frame subxids.
