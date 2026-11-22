# M0149\-0001h1a71 — typed literals gram.y accepts

## Problem

Only a fixed list of type names folded into a typed literal \(`date 'x'`\). `uuid 'x'`, `inet 'x'`, `bit '1'`,
`double precision 'x'`, `numeric(5,2) 'x'`, `pg_catalog.date 'x'` and similar were syntax errors, and an invalid
`interval 'x'` was a syntax error instead of interval\_in's error at the string.

## What PG does

gram.y's AexprConst accepts `ConstTypename Sconst`, `func_name Sconst` and `func_name '(' func_arg_list ')' Sconst`
and builds them with `makeStringConstCast`: a cast of the string, which coerce\_type later converts through the
type's input function, reporting a bad value at the string.

## Design

The lexer adapter's TYPEDLIT fold keeps its short forms \(knownTypeNames, still TypedStringLit\) and gains
`peekTypedLitForm`:

- a type with modifiers, `name ( n [, m] ) 'x'`, for the types in `typmodLitTypes`;
- `pg_catalog.name 'x'` for a known type name;
- `double precision 'x'`;
- `name 'x'` for the types in `castLitTypes` \(uuid, inet, cidr, macaddr, money, bytea, bit, varbit, xml, reg\*, …\).

The grammar action `typedLitExpr` turns these into a `CastExpr` of a `StringConst` positioned at the string, so the
existing cast input and error positions apply. An `interval` body that is not an interval becomes the same cast.

The parse stays limited to listed names. A general `IDENT Sconst` fold would also capture FDW option lists
\(`OPTIONS (host 'x')`, generic\_option\_elem\), which gram.y separates by context.

## Not covered

- User\-defined and unknown type names \(`mood 'happy'`, `nosuchtype 'x'`\), `nchar`, `national character` and
  `tid` \(M0149\-0001h1a85\). `tid` waits for M0149\-0001h1a84: brin.sql loops until `curtid > tid '(2, 0)'`, and
  `INSERT ... RETURNING ctid` returns NULL.
- A string cast to `bit(n)` or `varbit(n)` raises where PG pads or truncates \(M0149\-0001h1a83\).

## User and unknown types, qualified names, nchar \(M0149\-0001h1a85\)

- gram.y's `AexprConst: func_name Sconst` is now the grammar rule `IDENT SCONST` in c\_expr, so `mood 'happy'`
  reads a literal of a user type. Because it is a grammar rule, it applies only where an expression is expected:
  `OPTIONS (host 'x')` and the other option lists keep parsing as before, and goyacc reports no new conflicts.
- A qualified name, `schema.type 'x'`, is folded by the lexer for any schema, not only pg\\_catalog. A qualified
  name directly followed by a string appears nowhere else in the grammar. A qualified `IDENT '.' IDENT SCONST`
  grammar rule was tried and dropped: it shifted `.` over ColId and broke `x.i = y.j`.
- `NCHAR Sconst` and `NATIONAL TYPEDLIT` are bpchar literals.
- These casts carry `typedLiteral` and `typePos` \(dumped omitempty, so the other goldens are unchanged\). The
  analyzer checks that the type exists through the hook `analyzer.TypeNameExists`, which the executor fills with
  its regtype resolver, and raises 42704 `type "..." does not exist` at the type name, as typenameType does.
- The check is lenient where it has no session \(M0149\-0001h1a85a\). An unqualified name passes when a user type
  of that name exists in any schema, because search\_path is not available. `x[]` checks `x`, and the `float` and
  `dec` aliases pass.
- Every cast target is checked \(M0149\-0001h1a85a\): `'x'::nosuchtype`, `CAST('x' AS nosuchtype)` and
  `null::nosuch[]` raise 42704 at the type name. As in transformTypeCast, this happens before the operand is
  analysed. The message keeps the `[]`, as TypeNameToString does. Two resolver gaps had to be closed first:
  - Cast and column types accept array bounds: `int[2]` and `int[3][4]`. As in gram.y opt\_array\_bounds, the bound
    is not enforced. A domain over `int[2]` used to be a syntax error, so casts to it went unchecked. The grammar
    has one more known `'['` shift/reduce, since a cast type's array tail competes with a subscript; shift wins, as
    in PG. The Makefile count is now 61.
  - The information\_schema domains and their arrays \(sql\_identifier, cardinal\_number, character\_data,
    time\_stamp, yes\_or\_no\) resolve through regtype input, by qualified name or when information\_schema is on
    search\_path. They print qualified off the path \(`internal/catalog/information_schema_types.go`, OIDs as
    initdb seeds them\). Casts to them still evaluate as their base type's text: `'1'::cardinal_number` prints
    left-aligned.
- Not done: `tid 'x'` waits for M0149\-0001h1a84.
