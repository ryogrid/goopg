# Working set — inter-loop baton

Task: **M0145-0015 — CLOSED `[x]` as measured-no-gap.** The census extension is
the whole production change; no pull-up gate was widened, deliberately.

## Banner

Item 3: `… 0014 [x] → 0015 [x] → 0016 → 0017 → 0018`.
**Next selectable: M0145-0016** (`semianti-not-tail` leaf reorder). Read its
text FIRST — it says to verify subsumption by M0145-0005's slice (a) before
building the remap, and to close as subsumed if that slice already landed.

## The result

The census now reports `<kind>@<position>` — `top`/`not`/`or`/`scalar` —
because in PG the POSITION decides reachability, not the kind:
`pull_up_sublinks_qual_recurse` recurses AND and a NOT wrapper
(`prepjointree.c:789-845`) and then `/* Stop if not an AND */ return node;` at
`:877`, so OR args are never recursed.

```
SF0.25 knob arm, 99 queries:
  SubqueryExpr@scalar  29     ExistsExpr@or  4     InExpr@or  2
  NONE at @top, NONE at @not   -> the pullable subset is ZERO
```

Every residual decline is one upstream makes too. Both fallbacks the task said
to check for already exist and are LIVE:
`internal/optimizer/exists_to_any.go` (goopg's `convert_EXISTS_to_ANY`,
default ON) and `internal/executor/subplan_hash.go` (the hashed ANY probe,
default ON, reached from `evalInExpr`).

**Why no gate was widened**: moving an `@or` conjunct would make goopg convert
a sublink PG leaves as a SubPlan — a divergence the corpus value gates cannot
see, because the rows would still be right.

**Ledgered adjacent divergence**: goopg's hashed subplan degenerates PG's
partial-match table to a single "inner contained a NULL" bit. Sound only while
IN test expressions stay single-column; revisit when row-valued IN lands.

## Next step

**M0145-0016.** Its own text: "M0145-0005's remaining slice (a) makes semi/anti
real numbered leaf items, which retires the synthetic-slot contract by
construction — if that slice landed first, verify this decline is gone on the
corpus and close this task as subsumed rather than building the remap." So the
first move is a census check for `semianti-not-tail` (it was 6 on the last
run, not 3 — confirm on a fresh run), not code.

## Traps carried forward

- **Re-read the banner every loop.**
- When a panic names a mechanism, print the coordinates that WERE available
  before accepting that mechanism as the culprit (cost a loop on 0014).
- A new hand-written Expr type switch fails `TestExprSwitchInventoryIsPinned`;
  pin it AND add a ledger row.
- The RALPH_LOOP write-guard trips on prose mentioning a client tool and a
  reference port in one heredoc — split the write in two.

## Gates run

units PASS; tpch-spotcheck PASS (Q12=2 Q13=33); TPC-DS SF0.25 default arm
PASS=96 MISMATCH=0 CKMISMATCH=0 ERROR=0 TIMEOUT=0, plans 99/99 identical,
runtime-moves=0; TPC-H acceptance arm 24 MATCH; the commit hook's smoke runs
on commit.

## In-flight

none
