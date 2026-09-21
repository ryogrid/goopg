(idle — nothing in flight)

# Loop #64 result — bpchar-concat-operator FIXED; the whole bpchar class is CLOSED

Banner: items 0-9 unchanged (item 3 blocked by M0145-0001 `[!]`;
`partition_aggregate` is `[!]` awaiting an owner ruling). First open item-10
task in document order was `bpchar-concat-operator` — the 13th and last
divergence the loop-63 audit measured.

## The sibling problem, and how it was solved
The rule needs the operand's DECLARED type (a padded bpchar datum is
indistinguishable from a text value that genuinely ends in spaces), and
goopg evaluates a binary operator through TWO engines:
- interpreted `evalExprSlot` — HAS the operand expression;
- compiled `evalFastExpr` — does NOT; by eval time only Datums remain.

Fixed by capturing `declaredBpcharTypmod` for each side at COMPILE time
(`buildExprCtx`'s BinaryOp arm, into the previously-unused
`payload[8:12]`/`[12:16]` of a 40-byte field) — compile time is the LAST
point the operand expression exists. Both twins then call one shared helper
`concatOperandsAsText`, so the RULE cannot drift; only the route to the
width differs.

REJECTED and recorded: threading operand exprs into `evalBinary` (~40
callers, hot path, and the compiled twin has no expr to pass).

## Test design is the transferable part
The test drives BOTH twins explicitly instead of going through SQL, because
a SQL test exercises whichever evaluator the builder happens to pick and can
pass with one twin still broken. Non-vacuity verified PER TWIN: zeroing only
the compiled payload fails exactly the compiled assertions while the
interpreted ones still pass (the Rule #2 split, reproduced on purpose);
neutralising only the interpreted call fails exactly the interpreted ones.
A third case pins that a genuine `text` operand whose value really ends in
spaces KEEPS them — what makes strip-by-declared-type the only correct rule.

## Gates (all green)
units; FULL upstream regress suite (Rule #5) unchanged — only the known
`partition_aggregate`; tpch-spotcheck Q12=2/Q13=33; tpcds-sf025
`PLAN-SHAPE same=99 changed=0`; acceptance arm 24/24 value-MATCH; pgbench
smoke via hook. End-to-end SQL witness on a scratch server matched PG:
`length(c || 'z')`=3, `length('ab'::char(6) || 'z')`=3, text column=5.
Scratch server stopped and removed.

## Next loop
Item 10 continues: **PgAmcheck003 x4** (AI-…-002..-005, "re-CREATE
EXTENSION amcheck after restart"), then PgoutputInterop x10 (-006..-015).
No bpchar work remains — all 13 measured divergences are closed.

## Owner escalations OPEN — three
1. M0145-0018 cost-model no-go. 2. M0145-0001 lineage exhausted (blocks all
of item 3). 3. partition_aggregate's inventory row marks a never-passing
case must-pass.
