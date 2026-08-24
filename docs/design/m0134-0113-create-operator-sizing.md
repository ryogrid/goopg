# M0134-0113 — `create_operator.sql`: sizing + DETAIL/HINT fix

**Status:** PARKED (`failed`, 0% parity). One contained fix landed; the
remaining gaps are each independent and multi-call-site, out of scope for a
single test-port loop.

## Oracle case

`postgres/src/test/regress/sql/create_operator.sql` exercises `CREATE
OPERATOR` end to end: successful definitions, all of the mandatory-field and
attribute-recognition error paths, the PG14+ postfix-operator removal, custom
lexer-edge-case operator names (`!=-`, `@#@`, `#*#`, `#@%#`, `======`),
privilege checks (schema/type/function USAGE-EXECUTE), the `=>` reserved-name
ban, commutator/negator self-reference and conflict-with-existing-pair
checks, non-lowercase quoted-attribute-name rejection, and `COMMENT ON
OPERATOR` / `DROP OPERATOR` existence-check ordering for the `NONE` (postfix)
argument form.

Sized live via `scripts/pg-regress-runner.sh -v create_operator` against the
PG 18.3 oracle: 0% parity, 256-line diff after the one fix below (from a
264-line diff before it). No single remaining item is contained — see
"Remaining gaps" below.

## Landed this loop

**`CREATE OPERATOR` missing-RIGHTARG error uses the wrong field.** PG's
`DefineOperator` (`postgres/src/backend/commands/operatorcmds.c:183-188`)
raises `errmsg("operator right argument type must be specified")` with
`errdetail("Postfix operators are not supported.")` — a **DETAIL** field.
goopg's equivalent (`internal/executor/operators_ddl.go:21697-21702`) set the
same message text but put the explanatory sentence in **Hint** instead of
**Detail** (`ExecError.Hint` vs `ExecError.Detail`, both wired to the wire
protocol in `internal/executor/expr.go:49-57`). One-line fix: swap the struct
field. Verified via the same regress-runner diff — this exact block of the
diff (`DETAIL:` vs `HINT:` on the "Postfix operators are not supported"
line) no longer appears in the residual diff.

## Remaining gaps (why this case is PARKED, not fixed)

None of the following is a small, contained fix; each is either a
multi-call-site engine feature or touches shared, high-blast-radius plumbing
that this loop's budget does not cover. Listed in diff order:

1. **Custom-operator invocation syntax is broken for several lexer edge
   cases.** `SELECT @#@ 24` (prefix application of a leftarg-omitted
   operator) and `SELECT !=- 10` (ditto for a custom `!=-` operator) both
   fail to parse in goopg (`syntax error at or near "expected expression (got
   @)"` / `(got !=)`) even though the corresponding `CREATE OPERATOR`
   succeeded. PG parses these as ordinary prefix-operator applications.
   goopg's expression parser evidently only special-cases a fixed set of
   built-in prefix operators (`-`, `+`, `NOT`, …) rather than the general
   "any operator with only a rightarg is a prefix operator" rule. Needs a
   parser-level investigation of how custom multi-char operator tokens are
   matched against the pg_operator registry during expression parsing.

2. **`BETWEEN` vs `<>`/`<=`/`>=` precedence is wrong.** PG parses
   `true<>-1 BETWEEN 1 AND 1` as `true <> (-1 BETWEEN 1 AND 1)` — i.e.
   `BETWEEN` binds *tighter* than the built-in comparison operators but
   looser than general (`Op`) operators (gram.y's precedence table:
   `BETWEEN` sits between `Op`/`LIKE` and the six named comparison ops).
   goopg instead parses it as `(true<>-1) BETWEEN 1 AND 1`, producing a type
   error (`operator <> has incompatible operand types "bool" and "int8"`)
   for all four such expressions in the file. This is a precedence-table
   bug in `internal/parser` (`select.go`'s expression-parsing precedence
   levels), not an evaluator bug — confirm against `postgres/src/backend/
   parser/gram.y`'s `%left`/`%nonassoc` operator-precedence declarations
   before touching goopg's table, since getting the relative order of
   `BETWEEN`, `Op`, and the six hard-coded comparison operators wrong is
   exactly the kind of change the practice card's "known traps" warn about
   for parser precedence changes.

3. **Zero privilege enforcement for `CREATE OPERATOR`.** PG requires
   `USAGE` on the schema, both argument types, and the return type, plus
   `EXECUTE` on the underlying procedure (`DefineOperator` calls
   `pg_namespace_aclcheck`/`pg_type_aclcheck`/`pg_proc_aclcheck`). All five
   `permission denied for {schema,type,function} …` cases in the file
   currently succeed silently in goopg — `operators_ddl.go`'s `CREATE
   OPERATOR` path (around the `s.OpFuncName.Name != ""` block at
   `operators_ddl.go:21697`) has no ACL check at all. This is a genuinely
   new engine feature (goopg's ACL infrastructure exists for tables/
   schemas/functions elsewhere — see `truncate-conflict privilege model`
   instinct — but was never wired into `CREATE OPERATOR`).

4. **Several `CREATE OPERATOR` validations are entirely missing:**
   - `SETOF` argument rejection (`SETOF type not allowed for operator
     argument`) — leftarg/rightarg accept a `SETOF` type silently.
   - Unrecognized-attribute `WARNING` (PG warns per unknown key,
     e.g. `invalid_att`; goopg is silent) and non-lowercase quoted attribute
     names (`"Leftarg"`, `"Rightarg"`, …) should each independently warn,
     compounding into the same `operator function must be specified` final
     error once all attributes are rejected as unrecognized.
   - `negator`/`commutator` conflict-with-existing-pair checks
     (`commutator operator = is already the commutator of operator =`,
     `negator operator <> is already the negator of operator =`) are not
     enforced — goopg accepts the redefinition silently.
   - Self-negator ordering bug: goopg checks "only boolean operators can
     have negators" *before* the self-reference check, so a non-boolean
     self-negator (`int4ne` with `negator = ===!!!` on itself) reports the
     wrong error (`only boolean operators can have negators` instead of PG's
     `operator cannot be its own negator`) — this ordering swap is closer
     to a contained fix than the others but is still gated behind item 3's
     ACL work in the surrounding test transactions, so a future loop should
     re-size just this sub-case in isolation rather than assume it is
     free-standing.

5. **`=>` is not rejected as an operator name at parse time.** PG's
   grammar disallows `=>` as an operator name outright (`syntax error at or
   near "=>"`) since it is reserved for named-argument syntax; goopg's
   lexer/parser currently accepts it as an ordinary multi-char operator
   token in this position with no diff shown (i.e. it silently proceeds —
   full behavior not yet characterized beyond "PG's syntax error is
   missing").

6. **`COMMENT ON OPERATOR` / `DROP OPERATOR` on the `NONE` (postfix) form
   have inconsistent existence-vs-postfix-ban error ordering.** PG's
   `DROP OPERATOR ###### (int4, NONE)` reports `postfix operators are not
   supported` (checked before existence), while goopg reports `operator
   does not exist: integer ###### none` (existence checked first, and with
   a literal `none` in the message rather than treating `NONE` specially).
   Similarly `COMMENT ON OPERATOR ###### (NONE, int4)` / `(int4, NONE)` /
   `(int4, int8)` all raise the correct-shaped errors in PG but succeed
   silently in goopg — `COMMENT ON OPERATOR` has no existence check for any
   of the three cases in this file. Needs the same "does the referenced
   operator actually exist" gate `DROP OPERATOR` already has, applied to
   `COMMENT ON OPERATOR` too, plus reordering the `NONE`-postfix ban ahead
   of the existence check on the `DROP` path.

7. **`RAISE INFO` is emitted at `NOTICE` wire severity, not `INFO`.**
   `internal/executor/plpgsql_runtime.go:1758-1772`'s `RaiseStmt` handling
   only special-cases `"warning"` (routed to `ctx.AddWarning`, WARNING wire
   severity); every other level (`notice`, `info`, `log`, `debug`) falls
   through to `ctx.AddNotice`, which always emits a hardcoded `NOTICE`
   severity field (`internal/postmaster/dispatch.go:3111-3120` and the
   `NoticeFlush` callback at `internal/postmaster/dispatch.go:621-625`, both
   `libpq.FieldSeverity: "NOTICE"`). Fixing this properly needs a
   severity-tagged notice queue (`Context.Notices` is `[]string` today;
   `AddNotice`/`TakeNotices`/`NoticeFlush` would all need a `level string`
   parameter) threaded through **every** propagation site — there are ~15
   `for _, n := range child.TakeNotices() { ctx.AddNotice(n) }` forwarding
   loops in `plpgsql_runtime.go` alone (nested-call notice bubbling), plus
   the two wire-emission sites in `dispatch.go` (simple + extended query
   paths) and one in `postmaster/copy.go`. This is exactly the kind of
   broad, mechanical-but-many-call-sites change this loop's ONE-task budget
   does not cover safely; a dedicated loop should do it as its own task,
   verifying with `go test ./internal/postmaster/... ./internal/executor/...`
   plus a manual psql RAISE INFO/LOG/DEBUG severity check (none of goopg's
   existing tests currently assert wire severity for these three levels).

## Resume point

Re-run `scripts/pg-regress-runner.sh -v create_operator` after any of the
above lands to confirm forward progress; item 2 (BETWEEN precedence) is
likely the highest-leverage next fix since it silently breaks correctness
for *any* query mixing BETWEEN with `<>`/`<=`/`>=`, not just this test file.
