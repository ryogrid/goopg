(idle — nothing in flight)

# Loop #63 result — bpchar-text-function-class CLOSED (12 of 13)

Banner: items 0-9 unchanged (item 3 blocked by M0145-0001 `[!]`; item 10's
`partition_aggregate` is `[!]` awaiting an owner ruling). First open
item-10 task in document order was `bpchar-text-function-class`.

## Method: audited by MEASUREMENT, not by reading
One query computing `length(f('ab'::char(6)))` over 22 candidate string
functions, run against the PG 18.3 reference (SELECT-only) and a private
goopg scratch on :5533, then diffed. 13 divergences, 9 already correct.

## The finding that mattered most: the rule is NOT uniform
`concat`, `concat_ws` and `format`'s `%s` **KEEP** the padding in PG
(measured 7 / 8 / 6 on a `char(6)` holding 'ab') because they take variadic
`any` and go through the type's OUTPUT function, not a bpchar->text cast.
goopg already matched on all three. Applying one rule everywhere — the
obvious reading of "text functions strip" — would have introduced THREE new
divergences. They are now pinned as NON-stripping so a later loop cannot
"fix" them into a consistency upstream does not have.

## Sibling miss caught by the enumeration
`length` was fixed in M0143-0007b slice 1; its aliases
`char_length`/`character_length` are a SEPARATE case in the same switch and
were still padded. Nothing in the corpus caught it — only the enumeration.

## Fixed (12)
repeat, char_length, character_length, ltrim, replace, translate,
split_part, left, right, reverse, quote_literal, quote_ident,
regexp_replace — via one `coerceBpcharArgDatum` normalisation per function
(regexp_replace reads its subject 4x; per-use patching would strip in some
branches only). Bytea branches untouched.

## Deferred, deliberately: `bpchar-concat-operator` (filed)
`length('ab'::char(6) || 'z')` is 3 in PG, 7 in goopg. `evalBinary`
(expr.go:1767) receives only Datums, and the declared type is what decides
this. The cheap fix — coerce at expr.go:1322 — is a **Rule #2 trap**:
`exprnode.go:436` is the OTHER evaluator's binary-op site and would keep the
old behaviour, a fast-path/interpreted split on a VALUE question. Fix BOTH
or neither. Witness recorded in the task.

## Gates (all green)
units; FULL upstream regress suite (Rule #5) unchanged — only the known
`partition_aggregate`; tpch-spotcheck Q12=2/Q13=33; tpcds-sf025
`PLAN-SHAPE same=99 changed=0`; acceptance arm 24/24 value-MATCH; pgbench
smoke via hook. Test non-vacuous: pre-fix `expr.go` fails exactly 13
subtests. Scratch server on :5533 stopped and removed.

## Next loop
Item 10 continues: `bpchar-concat-operator`, then PgAmcheck003 x4
(-002..-005), PgoutputInterop x10 (-006..-015).

## Owner escalations OPEN — three
1. M0145-0018 cost-model no-go. 2. M0145-0001 lineage exhausted (blocks all
of item 3). 3. partition_aggregate's inventory row marks a never-passing
case must-pass.
