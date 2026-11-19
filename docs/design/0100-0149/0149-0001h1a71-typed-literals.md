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
