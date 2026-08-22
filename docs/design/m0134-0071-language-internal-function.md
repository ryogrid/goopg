# M0134-0071 — `equivclass.sql`: `LANGUAGE internal` CREATE FUNCTION support

Status: accepted · Date: 2026-08-22 · Milestone: M0134-0071

## The case

`equivclass.sql` is a `failed` regress row (CSV
`docs/test-port/postgres-oracle-target-inventory.csv:63`). Sized at HEAD
(`05a3448a`) it is **594 diff lines, 11 hunks, 40 `^+ERROR` / 0 `^-ERROR`,
deterministic across two fresh-cluster runs** — genuinely failing, not stale.

The 40 error lines are dominated by one root cause and its cascade:

```
13  ERROR: operator = has incompatible operand types "int8" and "int8alias1"   ← cascade of A
10  ERROR: language "internal" is not supported (Stage A: plpgsql, sql)        ← ROOT A
 6  ERROR: operator family "integer_ops" does not exist for access method "btree" ← ROOT C
 6  ERROR: current transaction is aborted, commands ignored until end of ...   ← cascade of A
 4  ERROR: only boolean operators can have restriction selectivity             ← cascade of A
 1  ERROR: operator does not exist: =(int8,int8alias1)                         ← cascade of A
```

Root A (direct + cascade) = **34/40 lines (~85%)**.

## Root cause: goopg's CREATE FUNCTION language allowlist omits `internal`

`equivclass.sql` defines the `int8alias1`/`int8alias2` shell types by the
standard PG idiom — a set of `CREATE FUNCTION ... LANGUAGE internal AS '<cname>'`
declarations naming the `int8` I/O/comparison/hash routines (`int8in`, `int8out`,
`int8eq`, `int8lt`, `btint8cmp`, `hashint8`):

```sql
CREATE FUNCTION int8alias1in(cstring) RETURNS int8alias1 STRICT IMMUTABLE LANGUAGE internal AS 'int8in';
```

goopg's `execCreateFunction` (`internal/executor/operators_ddl.go:15953`) gates
the language on an allowlist at `:15966-15968` that holds only `plpgsql`/`sql`/`c`
— so every `LANGUAGE internal` declaration raises
`ERROR: language "internal" is not supported (Stage A: plpgsql, sql)`.

Because those functions never get created, `int8alias1`/`int8alias2` stay *shell
types* with no I/O or comparison behavior, so every later `=`/`<` operator over
them fails to create ("only boolean operators can have restriction selectivity"
— the proc is missing, so the operator's return type is unknown), and the
analyzer cannot resolve `ff = f1` (`42804 incompatible operand types`, 13
SELECTs). The whole `ec1`/`ec2` middle of the file — where the equivalence-class
plan-shape work actually lives — is invisible behind that error wall.

PG's shape (`./postgres/src/backend/commands/functioncmds.c`):
- `interpret_AS_clause` (`:866`): for `LANGUAGE internal`, the `AS` clause *is*
  the C symbol name — no SQL body, no lookup of a `pg_proc` prosrc string.
- `fmgr_internal_validator` (`catalog/pg_proc.c:746`): errors 42883
  (`ERRCODE_UNDEFINED_FUNCTION`, "there is no built-in function named \"%s\"",
  `pg_proc.c:770-771`) if the name is not a known builtin. (The
  `fmgr.c:588`-area code is `fmgr_internal_function`, the non-erroring lookup —
  a distinct function, cited correctly only after implementation.)
- Shell NOTICEs: `functioncmds.c:109` ("return type %s is only a shell") and
  `:257` ("argument type %s is only a shell") fire when the function's return
  type or an argument type is a shell type.

## The slice (Bucket A)

Allow `LANGUAGE internal`, bind `AS '<name>'` to the already-seeded pg_proc row,
and dispatch the call to the builtin implementation. **Not a full pg_proc
import** — the allowlist gate + the AS-name→builtin binding + a real dispatch
indirection only. Four parts, all contained:

1. **Allowlist**: `execCreateFunction` accepts `"internal"` (add it to the
   language gate at `operators_ddl.go:15966-15968`). Sibling pair: the SAME
   change in `execCreateProcedure` (`operators_ddl.go:16697`, "Stage B"
   wording) — a same-language fix must land in both to avoid a
   plpgsql/sql/c/internal asymmetry.
2. **AS-name binding**: resolve `AS '<name>'` against the pg_proc seed catalog
   (OID + HandlerName/prosrc already present: `int8in` 460, `int8out` 461,
   `int8eq` 467, `int8lt` 469, `btint8cmp` 842, `hashint8` 949 —
   `internal/initdb/pg_proc_seed_data.go:348-357,548` and `:19896`). Unknown
   name → 42883 (`there is no built-in function named "%s"`), mirroring
   `fmgr_internal_validator` (`pg_proc.c:746`).
3. **Dispatch**: a real `case "internal"` in `dispatchStoredRoutineByLanguage`
   (`internal/executor/plpgsql_runtime.go:341`) routing to the builtin by bound
   name. The existing `case "c"` stub at `:347-358` returns default values; the
   `internal` case must do *real* dispatch through the same `evalFuncCall`
   builtin switch (`internal/executor/expr.go:9465`).
4. **Shell NOTICEs**: emit the two NOTICE lines from CREATE FUNCTION when the
   return/argument type is a shell type (`functioncmds.c:109`/`:257`).

## Known risk (containment), to be verified in the implementer slice

`evalFuncCall` (`expr.go:9465`) is a name switch whose per-case bodies often
type-check argument shapes. A user routine bound to `int8eq` receives
`int8alias1`-typed Datums (a `like int8` binary-compatible alias). The
implementer MUST verify the `int8eq`/`int8lt`/etc. cases accept the alias
without an explicit coercion; if they reject, add a binary-coercible-arg
normalisation for the internal dispatch — do not paper over it with a silent
wrong-answer.

## Honest expectation

Bucket A does **not** flip `equivclass.sql` to PASS. It clears the error wall so
the file's real planner buckets surface: E (EC constant propagation to
base-relation index paths, `equivclass.c:process_equivalence` +
`generate_base_implied_equalities_const`), F (`X=X`→`IS NOT NULL`,
`equivclass.c:229-269`), G (sort elimination via `pathkey_is_redundant`), H
(self-join removal, `analyzejoins.c`), I (Merge Full Join — a deliberate
goopg no-Sort divergence), J (`enable_*` GUCs ignored), and the RLS policy
filter gap. A is the decompose-the-case slice; E is the natural follow-up (the
case's namesake behavior). CSV row stays `failed`/`pass_required=no` unless the
case reaches byte-parity; no `make regen-testport`.

## Bucket C (separate, recorded for a later loop)

`ALTER OPERATOR FAMILY integer_ops USING btree ADD` fails 42704 because goopg's
opfamily registry (`LookupUserOperatorFamily`, `internal/catalog/catalog.go:19179`)
holds only *user*-created families — the built-in `pg_opfamily` rows
(`integer_ops` btree+hash, `pg_opfamily.dat:50`/`:52`) are not modeled. 6/40
error lines. CONTAINED but independent of Bucket A; not shipped this loop.
