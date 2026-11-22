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
- Not done:
  - `'x'::nosuchtype` is still accepted: the check covers only typed literals.
  - `tid 'x'` waits for M0149\-0001h1a84.
