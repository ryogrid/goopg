# M0146-0005do — an expression member joins its equivalence class

Status: done (2026-10-03). Parent: M0146-0005 (banner item 3).

## The PG behaviour

`distribute_qual_to_rels` hands a mergejoinable equality to
`process_equivalence` (`./postgres/src/backend/optimizer/plan/initsplan.c:2545`,
`check_mergejoinable` at `:2821`). `process_equivalence`
(`./postgres/src/backend/optimizer/path/equivclass.c:179`) puts both sides
into one EquivalenceClass. Either side may be any expression; the class
records `ec_has_volatile` (`equivclass.c:830`), so a volatile member never
generates derived clauses. Members of different types share a class when
the operator belongs to one btree opfamily. int2, int4 and int8 share
`integer_ops` (`./postgres/src/include/catalog/pg_opfamily.dat:50`).

From the class, `generate_join_implied_equalities`
(`equivclass.c:1550` / `_normal` `:1721`) derives a join clause between
any two members on the two sides of a join.
`generate_base_implied_equalities_const` (`equivclass.c:1272`) states the
class constant against every member.

TPC-DS Q59 has `wss.d_week_seq = wss_1.d_week_seq - 52` and
`wss.d_week_seq = d.d_week_seq`. PG's class
`{wss.d_week_seq, d.d_week_seq, wss_1.d_week_seq - 52}` gives it
`(wss_1.d_week_seq - 52) = d.d_week_seq`, which its plan uses as a Hash Cond.

## What was missing

goopg had two class builders, and both took only `ColumnRef = ColumnRef` of
one type:

- the seam's closure, `inferEqualitiesClosure` in equiv_class.go, which
  supplies the transitive clauses and the constant propagation;
- joinrestrict.go's class assignment, which picks one clause per class
  per join (`reduceEquivClassJoinClauses`) and drives `oneClausePerEquivClass`.

The expression clause entered neither, so Q59 could never use the derived
clause.

## What landed

- **`ecEquality` / `ecMemberIdent` / `ecEqualityIdents` (equiv_class.go).**
  These widen the member test, mirroring `isColumnRefEquality` / `identOf`.
  - A ColumnRef keys as before.
  - Any other expression is a member when it is built from one relation's
    columns of this level, plus constants, operators and casts. It is
    walked with `walkExprRefs` under scopeVeto, so a sublink aborts the
    walk, and it must read at least one column.
  - Function calls are excluded: the planner cannot always resolve their
    volatility, and PG refuses volatile members from generating clauses.
  - The member is keyed by its `exprIdentityKey`.
- **Type rule.** Both sides must have the same type, using the resolver's
  `ResultType` for an operator. With an expression member, two integer
  types are also accepted, which is the integer_ops family.
  - goopg types an integer literal int8, so `int4 - 52` is int8 here while
    PG makes it int4.
  - The family rule admits it either way, and the executor already
    compares int4 with int8.
  - A cross-type column pair stays out, exactly as before.
- **Callers switched together** (hard-won rule 2):
  - `inferEqualitiesClosure`'s class build;
  - `joinrestrict`'s union / member-order / class-key recording;
  - `equivClassJoinClause`'s member-to-rel map and pair lookup.

## Verification

- Probe on PG 18.3 vs goopg with
  `e1.a = e2.b - 52 AND e1.a = e3.c AND e2.q < 500 AND e3.r < 300 AND …`:
  - goopg now plans exactly PG's tree, including
    `Hash Cond: ((e2.b - 52) = e3.c)` and identical row estimates.
  - With `AND e1.a = 7`, `(b - 52) = 7` filters e2, as in PG.
- `TestECExpressionMemberDerivesJoinClause` covers:
  - the derived clause and the constant reaching the expression;
  - two-relation expressions and function calls refused;
  - an int4/int8 column pair refused, and an int4 column = int8
    expression accepted.
- `TestECExpressionMemberValues` pins PG's values (148, 99, `7|59|7`).
- Q59 fires at SF0.25 and SF1 with values PASS: it now joins `d` to
  `wss_1` on the derived clause.
- SF1 categories moved within noise: join-method 27→28,
  parameterisation 35→36, qual-placement 8→7. match is unchanged and
  ea-ratchet stays at 10/10.
- Regress A/B against HEAD: equivclass, subselect, union, select, inherit,
  partition_join, aggregates, with and select_distinct are byte-identical.
  In `join`, an unordered result's row order flips, as on HEAD.
- Gates: units, spotcheck, SF0.25 sweep 96/96, fire set, TPC-H arm,
  ea-ratchet.

## Residuals (ledgered)

- **Q59 still differs from PG.** PG joins `{wss, store, d}` to
  `{wss_1, store_1}` on two conditions, then joins `d_1` by Nested Loop
  over a Materialize. goopg joins `d` to `{wss_1, store_1, d_1}` first.
  Filed as M0146-0005ds.
- **Rows disagree above Q59's final join.** goopg's Sort above the final
  join now shows 4811 rows over a Hash Join of 15. The rel's size comes
  from one split and the path's from another. 13 other queries in the
  baseline (Q43, Q46, Q47, Q57, …) already show this disagreement.
- **Not ported:**
  - multi-relation expression members;
  - members containing function calls (a volatility check against the
    routine registry would admit the immutable ones);
  - cross-type column pairs within one btree family;
  - EXPLAIN operand orientation (`ec_clause_orient.go`) for clauses with
    an expression member, which keeps the order the closure emits.
