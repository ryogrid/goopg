# M0146-0016 — presorted split admits non-column group keys

## Task

`transportGroupSortKeys` (M0146-0003 S6, `internal/optimizer/groupclause.go`)
required every group expression to be a bare `*ColumnRef` — the transport
position had no honest `Sort Key:` name otherwise, so the presorted arm
(`Finalize GroupAggregate -> Gather Merge -> Sort -> Partial HashAggregate`,
planner.c:7704-7724) declined whenever a `GROUP BY` item was an expression.
PG's arm carries arbitrary group expressions: the transport position IS the
merge key.

## Corpus measurement (the task's admission gate)

Scanning the M0146-0001 PG18.3 reference captures for the presorted shape
(`Finalize … -> Gather Merge -> … -> Partial …`) and checking each merge
`Sort Key:` for a non-column term:

| corpus | queries with presorted split | non-column keys |
|--------|------------------------------|-----------------|
| TPC-DS SF0.25 | 10 (Q19 Q40 Q43 Q62 Q66×2 Q76 Q77×2 Q85 Q99) | Q62, Q76, Q99 |
| TPC-DS SF1    | 12 (+ Q71, Q73, Q77 arm)      | Q62, Q76, Q99 |
| TPC-H         | 5 (Q1 Q4 Q5 Q12 Q15)        | none |

Q23 (`GROUP BY substr(i_item_desc,1,30), …`) is NOT a consumer — PG itself
elects the hashed split there. The measured consumers at both scales:

- **Q62 / Q99** — `GROUP BY substr(w_warehouse_name,1,20), sm_type, …`:
  PG elects `Finalize GroupAggregate -> Gather Merge -> Partial
  GroupAggregate -> Sort`; goopg filed only the hashed split.
- **Q76** — PG's transport keys include constant literals
  (`('store'::text), ('ss_customer_sk'::text)`) — in goopg the group
  exprs are ColumnRefs into the UNION arm output, so this one was
  already admissible; its residual divergence is a different axis.

## Change

`transportGroupSortKeys` now synthesises, for a non-column group
expression, a positional `*ColumnRef` {Index: k.Pos, Name/Type/
SourceTableIdx copied from `agg.Output()[k.Pos]`}. Bare-ColumnRef group
exprs keep the existing clone-and-rebind path verbatim. Fail-closed when
a position is out of range, the schema is missing/short, or the slot is
unnamed.

The render side needed nothing new: `sortGroupKeySource` (R66 Arm S)
resolves a `Sort Key:` ColumnRef positionally through the child
aggregate's `GroupExprs`, so the worker Sort prints PG's
`Sort Key: (substr(warehouse.w_warehouse_name, 1, 20))` — the expression
itself, S18-wrapped.

## Result

- Q62/Q99 SF0.25 + SF1: `Finalize GroupAggregate -> Gather Merge -> Sort
  -> Partial HashAggregate` elected; fireset fires = {Q62, Q99} at both
  scales, executions PASS (oracle rows: 100 / 90).
- Census categories on the fires dropped `parallelism` (SF0.25 67->65,
  SF1 74->72). Remaining diff classes on those queries are the
  pre-existing join-order/cost axes plus `sort-strategy`: PG elected its
  OTHER presorted variant — sorted-input `Partial GroupAggregate` over a
  worker `Sort` of the input — while goopg files the hash-then-sort
  variant (`Sort -> Partial HashAggregate`). That sorted-input partial
  arm is a separate admission task, not this slice.
- Q76 unchanged: the arm was already admissible (column group keys); the
  hashed split won on cost before and still does.
- Q8 etc. untouched: `introduced=none` at both scales.

## Tests

- `TestUpperSplitSortedTransportArmExprKey` (optimizer): a named literal
  group key files the sorted arm; worker-Sort pathkeys are the
  positional refs `{Index:0 Name:"?column?"}` + the cloned `g1`.
- `TestUpperSplitSortedTransportArmRefusals` (optimizer): the literal
  half re-pinned as the fail-closed arm — no output schema to name the
  transport position => decline; the DISTINCT-agg refusal is unchanged.
- `TestSortKeyTransportExprKeyRendersSource` (executor): a positional
  key over a partial agg renders `(substr(w_name, 1, 20))` — the group
  expression, not a slot alias — via `sortKeyParts`/Arm-S.
- `TestPartialEmitSortedIdentity` gained `sorted-gathermerge-exprkey`:
  serial-vs-merged row identity with a `substr` group key at workers
  1/2/4.

## Deferred (ledger-worthy residue)

- PG's sorted-input partial variant (`Partial GroupAggregate` over a
  worker `Sort` of the input, the Q62/Q99 winner upstream) is a second
  presorted arm goopg does not file at all — `sort-strategy` category on
  the fireset captures it.
