# M0149-0001h1a14 — the reg* input/output port (regproc.c)

Covers M0149-0001h1a12, h1a13 and h1a14, which ported most of
`postgres/src/backend/utils/adt/regproc.c` onto one executor entry point.

## One input port

`regIdentifierInput(v, typeName, ctx, pos)`
(`internal/executor/reg_identifier.go`) is the only text → OID path for the
reg types. It serves:

- `'x'::regfoo` casts (the CastExpr arms in `expr.go`);
- the one-argument function forms `regfoo('x')`, which PG resolves as a cast
  (`func_get_detail`, FUNCDETAIL\_COERCION);
- `to_regfoo('x')`, which wraps the input in a soft error context and
  returns NULL on any error;
- `pg_input_is_valid` / `pg_input_error_info`;
- reg-typed column coercion and COPY FROM, through
  `isRegIdentifierTypeName`.

regproc and regprocedure used to have a second, divergent copy of the
lookup in the cast arm. h1a13 deleted it (Hard-won Rule \#2).

| type | PG function | goopg arm |
|---|---|---|
| regclass, regtype, regrole, regcollation, regnamespace | `*in`, via `parseDashOrOid` | `regIdentifierInput` |
| regproc | `regprocin`: a unique name, else 42725 | routines, then the full `pg_proc` seed |
| regprocedure | `regprocedurein`: `parseNameAndArgTypes`, exact signature | `regParseNameAndArgTypes` for syntax errors; `LookupProcForNode` over the arg OIDs |
| regoper | `regoperin`: `parseNumericOid` (no `-`), `OpernameGetCandidates` | `regoperIn` (`regoper_input.go`) |
| regoperator | `regoperatorin`: `parseNameAndArgTypes(allowNone)`, `OpernameGetOprid` | `regoperatorIn` |

Operators resolve over the full 799-row `pg_operator.dat` seed
(`catalog.BuiltinOperatorsByName` / `BuiltinOperatorEntryByOID`), not the
five-row curated `builtinOperatorsByKey`. User operators come from
`InMemory.UserOperatorsByName`.

- A bare name searches pg\_catalog first, then the visible schemas. An
  earlier operator masks a later one with the same (left, right) signature,
  as `OpernameGetCandidates` does.
- An explicit schema restricts the search to that schema. A schema that
  does not exist yields no candidates (`missing_schema_ok`), so the error is
  42883 "operator does not exist", not 3F000.

## One output port

`regOutShared` renders every reg type for SELECT (`postmaster/dispatch.go`),
COPY TO (`copy_text.go`) and reg → text casts. regoper and regoperator
delegate to `regoperOut` / `regoperatorOut`:

- InvalidOid renders as `0`, and an unknown OID as its number.
- regoper renders the bare name when `regoperin` would find exactly this
  operator, otherwise `schema.name`.
- regoperator renders `name(lefttype,righttype)`, with each type in
  `format_type_be` form and `NONE` for a missing operand.

## Not covered (deferral ledger)

- regconfig and regdictionary: goopg has no text search catalog.
- `parseTypeString` error forms in regtype: a multi-word name, a bad typmod,
  or too many dotted names.
- In regprocedure, the argument list is checked for syntax, and resolved
  exactly only for builtins. User routines still resolve by name.
