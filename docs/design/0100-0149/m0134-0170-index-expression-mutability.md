# M0134-0170 — index expressions and predicates must be IMMUTABLE

Status: accepted (2026-08-29)
Milestone task: M0134-0170 (`sqljson_queryfuncs.sql`)
Code: `internal/executor/index_mutability.go` (new),
`internal/executor/pg_nonimmutable_builtins.go` (new),
`internal/executor/operators_ddl.go` (`execCreateIndex`),
`internal/executor/operators_ddl_partition.go` (`validatePartKeyExprInner`)
Harness: `scripts/pg-regress-runner.sh` (two operational guards — see §5)
Guard: `internal/executor/index_mutability_test.go`

## 1. The case, sized honestly

`sqljson_queryfuncs.sql` was run for the first time this loop
(`not-tried` → **`failed`**): **2021 diff lines, 259 `^+ERROR`, 113
`^-ERROR`**. Its composition:

| bucket | lines |
|---|---|
| `syntax error at or near "RETURNING"` (121), `"PASSING"` (38), `"DEFAULT"` (11), `"WITH"`/`"WITHOUT"`/`"EMPTY"`/`"OMIT"`/`"FORMAT"`/… | 218 `^+ERROR` |
| `function json_exists/json_value/json_query does not exist` | 33 `^+ERROR` |
| `relation "…" does not exist` cascade from the above | 8 `^+ERROR` |

That is **100% of the case**, and all of it is the one REFACTOR-tier missing
subsystem already ledgered as **0168a**: goopg's grammar carries no SQL/JSON
query-function family (`JSON_EXISTS`, `JSON_VALUE`, `JSON_QUERY` and their
`PASSING` / `RETURNING` / `{ERROR|NULL|DEFAULT …} ON {ERROR|EMPTY}` /
`{KEEP|OMIT} QUOTES` clause vocabulary). This is the third case in a row
parked on that same blocker (0168, 0169, 0170), exactly as the 0168 ledger row
predicted. **The case is PARKED and its CSV row stays `failed`.**

## 2. What the case pointed at

`sqljson_queryfuncs.sql:360-389` is a block upstream labels *"Test mutabilily
of query functions"*: 39 consecutive `CREATE INDEX ON
test_jsonb_mutability (JSON_QUERY(js, '…'))` statements, **28 of which**
PostgreSQL rejects with

```
ERROR:  functions in index expression must be marked IMMUTABLE
```

because a `jsonpath` naming `.datetime()`/`.time_tz()` resolves to a *stable*
`jsonb_path_query` overload. goopg cannot parse `JSON_QUERY` at all, so all 39
are unreachable here — but the error class they assert is not
SQL/JSON-specific, and **goopg did not implement it anywhere**:

```sql
-- all four ACCEPTED by goopg before this change; all four rejected by PG 18.3
CREATE INDEX ON t ((clock_timestamp()::text));
CREATE INDEX ON t ((a + (random()*10)::int));
CREATE INDEX ON t (my_volatile_fn(a));
CREATE INDEX ON t (a) WHERE a > (random()*10)::int;
```

This is not cosmetic. An index entry computed from a non-IMMUTABLE expression
cannot be reproduced at probe time, so the index **silently disagrees with the
heap** and an index scan returns different rows than the seq scan over the same
predicate would. Upstream's own comment
(`postgres/src/backend/commands/indexcmds.c:2010-2015`) says it plainly: *"if
you aren't going to get the same result for the same data every time, it's not
clear what the index entries mean at all"*.

Upstream sites, both raising `ERRCODE_INVALID_OBJECT_DEFINITION` (42P17) out
of `contain_mutable_functions_after_planning()`:

| site | file:line | covers |
|---|---|---|
| `CheckPredicate()` | `indexcmds.c:1843-1857`, called from `DefineIndex` `:906` | the `WHERE` clause of a partial index |
| `ComputeIndexAttrs()` | `indexcmds.c:2016-2019` | every non-`Var` index key expression |
| `ComputePartitionAttrs()` | `tablecmds.c:19966` | partition key expressions (42P16) |

## 3. The sibling-path finding

goopg had **one of the three ports**: `validatePartKeyExprInner`
(`operators_ddl_partition.go:283`) implemented the partition-key half. The two
index halves did not exist. That is the recurring *sibling code paths must stay
in sync* pattern — one port of an upstream predicate written, the other never
started — and it is why this bug survived: nothing in goopg claimed the index
check existed, so nothing looked wrong.

Auditing the surviving sibling for the shared implementation turned up a
**second, independent gap in it**: it consulted only `ctx.Catalog.Routines()`,
i.e. *user-defined* functions, plus a 14-name hand list. A bare volatile
**built-in** in a partition key —

```sql
CREATE TABLE t (a int) PARTITION BY RANGE ((a + (random()*10)::int));
```

— was accepted. Both halves now share one classifier
(`exprHasNonImmutableFunction`), so the partition path is fixed by the same
change.

## 4. The classifier

`funcCallIsNonImmutable` (`index_mutability.go`) resolves each call in
upstream's name-lookup order:

1. **A user-defined routine of that name wins.** A `LANGUAGE sql` routine is
   *inlined* by PG before the volatility test, so its declared marker is
   irrelevant and the **body** is scanned instead — this is why PG accepts
   `CREATE FUNCTION f(int) RETURNS int VOLATILE LANGUAGE sql AS 'SELECT $1'`
   in an index expression but rejects the identical body written in plpgsql.
   Any other language is taken at its declared `provolatile`.
2. **Otherwise it is a built-in**, classified by `nonImmutableBuiltinNames`.

### Why a *name* set, and why it is deliberately asymmetric

goopg's DDL-time trees (`parser.FuncCall`) are unresolved: no overload
resolution, no argument-type inference. PG asks the resolved `pg_proc` row
(`func_volatile(funcid) != PROVOLATILE_IMMUTABLE`); goopg can only ask the bare
name. **31 `pg_proc.dat` names carry both immutable and non-immutable
overloads** (`date_trunc`, `to_timestamp`, `age`, `length`, `timezone`,
`extract`, `quote_literal`, …). Listing them would reject index expressions
PostgreSQL *accepts* — `date_trunc('day', <timestamp>)` is IMMUTABLE — which is
strictly worse than missing one: it breaks working DDL rather than admitting
DDL PG would have rejected. So a mixed-volatility name is **excluded** and
treated as immutable. The residual false-negative is ledger **0170a**.

The table is derived mechanically (group `pg_proc.dat` by `proname`; keep a
name iff no entry declares `provolatile 'i'` — `BKI_DEFAULT(i)` means an
omitted field is immutable) and **re-derived from the in-tree oracle by
`TestNonImmutableBuiltinsMatchPgProcDat`**, so it cannot silently drift from
PG 18.3.

### The walker

`walkParserExprFuncCalls` covers every `parser.Expr` implementation, not the
five the partition path used to. An index expression is arbitrary user SQL, so
a gate that misses one container node is trivially bypassable:

```sql
CREATE INDEX ON t ((CASE WHEN true THEN random() ELSE 0 END));   -- CASE arm
CREATE INDEX ON t ((ARRAY[a, (random()*10)::int]));              -- ARRAY element
```

Sub-SELECT wrappers are deliberately **not** descended into: PG rejects a
subquery in an index expression or predicate much earlier, in `transformExpr`'s
`EXPR_KIND_INDEX_*` handling, so a function inside one is unreachable by this
gate upstream too.

### Placement

Both calls sit where upstream puts them, and the ordering is load-bearing for
the same reason M0134-0160 established for the reloption check: the checks run
**before** `index_create`'s name-conflict test, so a negative case that reuses
an index name keeps reporting the mutability error instead of degenerating into
"relation already exists".

- predicate → straight after the AM-capability block, mirroring
  `DefineIndex`'s `if (stmt->whereClause) CheckPredicate(...)` (`:905-906`);
- key expressions → after `index_reloptions`, mirroring `ComputeIndexAttrs`.

## 5. Two harness guards (`scripts/pg-regress-runner.sh`)

Sizing this case the first time produced a **fabricated number**, and the
runner reported it as a normal result. Both failure modes are now fatal:

1. **Port already in use.** `--auto-start` bound nothing when something already
   answered on 15435 (a stale orphan, or a concurrent A/B worktree's server);
   the readiness probe was satisfied by *that* server, every `psql` silently
   measured it, and `cleanup()` — keyed on an empty `SERVER_PID` — never reaped
   anything. Observed live: `sqljson_queryfuncs` "sized" at 1291 diff lines
   that were 1290 expected-only lines plus one `connection refused`. The runner
   now refuses to auto-start on a busy port, and fails if its own
   `postmaster.pid` holds no live pid.
2. **`psql` exit status 2** — *"connection to server went bad and the session
   was not interactive"* (`doc/src/sgml/ref/psql-ref.sgml`, Exit Status) — is
   now a new outcome class `ERROR`, not a diff. The capture in that case is a
   truncated prefix plus one libpq line, which diffs against the whole expected
   file and yields a believable-looking case size. Ordinary in-case SQL errors
   do not set this (no `ON_ERROR_STOP`), so the guard only fires on real
   connectivity loss.

The lesson is the recorded one in a new place: *a measurement harness that
cannot fail loudly will report a plausible number instead*, and that number
then gets written into `fix_plan.md` and the deferral ledger as fact.

## 6. Verification

- **Guard** `internal/executor/index_mutability_test.go` — 10 rejection
  subtests (each asserting SQLSTATE 42P17 and the exact upstream message), 6
  acceptance statements including the two canaries (`date_trunc('day', c)` for
  the mixed-volatility exclusion, an inlined `LANGUAGE sql` body for the
  inlining rule), plus the partition-key sibling and the `pg_proc.dat`
  re-derivation. **Revert-checked**: with the two call sites and
  `index_mutability.go` removed, all 10 index subtests and the partition test
  fail.
- **Why a unit test and not a regress case:** `grep` over
  `postgres/src/test/regress/expected/` finds the two index messages in
  **exactly one file — `sqljson_queryfuncs.out` (28 occurrences)** — the case
  that cannot run. No other ported case exercises them.
- **Regress A/B vs a HEAD worktree, 14 DDL-heavy cases**
  (`create_index`, `create_index_spgist`, `create_table`, `create_table_like`,
  `alter_table`, `indexing`, `constraints`, `inherit`, `insert_conflict`,
  `replica_identity`, `reloptions`, `hash_index`, `brin`, `gist`): identical
  line counts throughout, **13 byte-identical**, zero regressions. The
  fourteenth (`create_index`) differs only in a **nondeterministic Go pointer
  address** printed inside `pg_get_indexdef`'s predicate deparse
  (`WHERE (c1::text > &{105 0x33b043b678c0 C})`) — present in both builds and
  in every run, a pre-existing deparse defect ledgered as **0170c**.
- The case itself is **unchanged at 2021 lines** by this fix, as expected: its
  mutability block is `JSON_QUERY`-only and never parses.

## 7. Deferred

| id | what |
|---|---|
| 0168a | the SQL/JSON subsystem that blocks the case as a whole (shared with -0168/-0169; here it is the query-function family `JSON_EXISTS`/`JSON_VALUE`/`JSON_QUERY`) |
| 0170a | mixed-volatility names (`date_trunc`, `to_timestamp`, `age`, …) are excluded from the built-in set, so the *stable* overload of such a name is still accepted in an index expression; needs argument-type inference at DDL time |
| 0170b | goopg does not raise `function … does not exist` for an unknown name in an index expression; unknown names are treated as immutable here |
| 0170c | `pg_get_indexdef` deparses a partial-index predicate's right-hand side as a Go value (`&{105 0x… C}`) instead of the literal |
