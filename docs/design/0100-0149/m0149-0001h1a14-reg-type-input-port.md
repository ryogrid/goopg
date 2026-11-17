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

## search\_path and object visibility (M0149-0001h1a32)

The input side follows the session's `search_path` the way namespace.c does:

- regproc / regprocedure (`resolveUserRoutineOnPath`): a qualified name
  searches its schema only; a bare name walks the path in order.
  regprocedure takes the first routine whose input argument types (OUT
  excluded) equal the parsed list. regproc keeps one routine per signature,
  the earliest on the path, and raises 42725 when more than one remains.
- A builtin regprocedure matches the exact signature first
  (`builtinProcByArgs` over `catalog.BuiltinProcOIDsByName`; argument
  typnames resolve through `catalog.BuiltinTypnameOID`). The name-only
  `LookupBuiltinProc` shortcut used to answer one overload per name, so
  `'exp(double precision)'` reached exp(numeric).
- regtype: a user type found by name must sit in the qualifier's schema,
  or for a bare name in a schema on the path (`userTypeOnPath`). Multi-word
  names (`double precision`, `timestamp with time zone[]`), quoted keyword
  typnames (`"char"` is char, not bpchar; `"any"`) and `_elem` array typnames
  resolve as `parseTypeString` would.

The output side qualifies per object: `RegOutVisible` passes the session
predicate to `regOutShared` both as the qualify rule and for the
regprocedure argument list. SELECT and COPY TO use it whenever they hold the
predicate, instead of the single "public is off the path" flag. A builtin
argument typname renders as `format_type_be` prints it (`aclitem[]`,
`"any"`).

`pg_table_is_visible`, `pg_type_is_visible`, `pg_function_is_visible` and
`pg_statistics_obj_is_visible` (`pg_is_visible.go`) are the
`*IsVisibleExt` functions: NULL for a missing OID; otherwise the object's
bare spelling must resolve, through the same input functions, to the same
OID, so an earlier schema's same-named object hides it. Statistics objects
walk the path directly. All 3397 builtin functions round-trip on the default
path (`TestBuiltinFunctionsAreVisible`).

## Not covered (deferral ledger)

- regconfig and regdictionary: goopg has no text search catalog.
- `parseTypeString` error forms in regtype: a bad typmod or too many dotted
  names. A bare `any` is accepted, where PG raises a syntax error.
- The implicit `pg_catalog`-first rule is not applied to user routines: a
  user routine with a builtin's name and signature resolves before the
  builtin.
- `pg_operator_is_visible`, `pg_opclass_is_visible`,
  `pg_opfamily_is_visible`, `pg_conversion_is_visible`,
  `pg_collation_is_visible` and the text-search kinds still answer true.
- Error cursor positions for a failed reg cast point at `::`, not at the
  literal.
