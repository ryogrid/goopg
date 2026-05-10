# Design 0075-0001 — Q5 equivalence-class inference for transitive equalities

**Milestone:** M0075-0001
**Status:** **PARTIAL — module + 9 unit tests landed
2026-05-10; the planner-side hook into `tryBushyDP` was
attempted then reverted. Empirical result: enabling the
synthesised conjuncts caused Q9 to cancel at the 600 s
budget (was 219-256 s baseline mode-1). The
`inferTransitiveEqualities` function works correctly in
unit tests (all 9 PASS), but feeding the closure
predicates into the bushy DP enumerator changes Q9's
join graph in a way that pessimises plan selection —
the additional edges expose join orders the cost model
ranks higher than the current good plan. Investigation
deferred to M0076.** The module lands as forward-compat
infrastructure (`internal/planner/equiv_class.go` +
`equiv_class_test.go`) so M0076's investigation can
build on a tested, correct closure implementation.
**Owner:** TBD
**Branch:** `gc-oriented-refactor` (continuation)
**Depends on:** none (pure planner-side addition).

## Context

TPC-H Q5 cancels at 1100s on goopg SF=1; theoretical
wall time at SF=1 is ~5-10 s. Phase-6 §4.1 identified
plan-level work as the bigger lever.

Research (Explore agent, 2026-05-10) confirmed the
specific gap: **goopg has NO transitivity inference**.
Q5's WHERE clause contains:

```sql
WHERE c.c_nationkey = s.s_nationkey
  AND s.s_nationkey = n.n_nationkey
```

PostgreSQL infers `c.c_nationkey = n.n_nationkey` via
its EquivalenceClass machinery, opening join-order
alternatives. goopg's planner sees only the literal
predicates and emits join orders constrained to the
exact graph, missing potentially-faster shapes.

Search confirmed: no "transitivity", "EquivalenceClass",
"infer", "equiv class", "implied" symbols in
`internal/planner/`.

## Goals

- New planner pass `inferTransitiveEqualities(conjuncts,
  schema) []Expr` that:
  - Walks WHERE conjuncts.
  - Identifies equality predicates between ColumnRefs
    (`a = b` where a, b both planner.ColumnRef).
  - Builds equivalence classes via union-find.
  - Synthesizes the closure (`a=b ∧ b=c` adds `a=c`)
    as new BinaryOp{Op: OpEq, Left: a, Right: c}
    conjuncts NOT already present.
- Hooks after `pushPredicatesIntoCrossJoins`
  (`pushdown.go:31`) and before `enumerateBushyPlans`
  (`bushy.go:344`) in `planner.go::Plan`.
- Synthesised conjuncts feed both filter pushdown AND
  join-order DP, exposing additional join edges.

## Non-goals

- **Equivalence classes spanning constants.** Postgres
  also infers `a=b ∧ a=5 → b=5` (constant propagation).
  M0076 candidate.
- **Equivalence classes through expressions.** E.g.,
  `a+1 = b+1 → a = b`. Out of scope.
- **Type coercion handling.** Equivalence classes only
  merge ColumnRefs of the same SQL type. Cross-type
  equalities (e.g., int4 = int8 with implicit cast)
  stay as literal predicates.

## Proposed implementation

### Data structure: union-find over ColumnRef identities

```go
// columnIdent uniquely identifies a column for
// equivalence-class tracking. Source-table-aware so
// `a.id` and `b.id` don't merge.
type columnIdent struct {
    name           string
    sourceTableIdx int16 // M0071-0009
    schemaPos      int   // optional disambiguator
    typeName       string
}

type equivClasses struct {
    parent map[columnIdent]columnIdent
    rank   map[columnIdent]int
}

func newEquivClasses() *equivClasses {
    return &equivClasses{
        parent: make(map[columnIdent]columnIdent),
        rank:   make(map[columnIdent]int),
    }
}

func (ec *equivClasses) find(c columnIdent) columnIdent {
    if _, ok := ec.parent[c]; !ok {
        ec.parent[c] = c
        return c
    }
    if ec.parent[c] != c {
        ec.parent[c] = ec.find(ec.parent[c])
    }
    return ec.parent[c]
}

func (ec *equivClasses) union(a, b columnIdent) {
    ra := ec.find(a)
    rb := ec.find(b)
    if ra == rb {
        return
    }
    if ec.rank[ra] < ec.rank[rb] {
        ra, rb = rb, ra
    }
    ec.parent[rb] = ra
    if ec.rank[ra] == ec.rank[rb] {
        ec.rank[ra]++
    }
}

// classes returns each equivalence class as a slice.
func (ec *equivClasses) classes() map[columnIdent][]columnIdent {
    result := make(map[columnIdent][]columnIdent)
    for k := range ec.parent {
        root := ec.find(k)
        result[root] = append(result[root], k)
    }
    return result
}
```

### Pass: inferTransitiveEqualities

```go
// inferTransitiveEqualities walks the WHERE conjuncts,
// identifies ColumnRef = ColumnRef equalities, builds
// equivalence classes, and returns synthesised
// closure predicates that are NOT already present in
// the input conjuncts.
func inferTransitiveEqualities(
    conjuncts []Expr,
) []Expr {
    ec := newEquivClasses()
    seenPairs := make(map[[2]columnIdent]bool)
    
    // Pass 1: build classes from literal a=b predicates;
    // record the explicit pairs.
    columnRefByIdent := make(map[columnIdent]*ColumnRef)
    for _, c := range conjuncts {
        if eq, ok := isColumnRefEquality(c); ok {
            la := identOf(eq.left)
            lb := identOf(eq.right)
            ec.union(la, lb)
            seenPairs[orderedPair(la, lb)] = true
            columnRefByIdent[la] = eq.left
            columnRefByIdent[lb] = eq.right
        }
    }
    
    // Pass 2: synthesise closure predicates for pairs
    // in the same class but NOT in seenPairs.
    var added []Expr
    for _, members := range ec.classes() {
        if len(members) < 2 {
            continue
        }
        for i := 0; i < len(members); i++ {
            for j := i + 1; j < len(members); j++ {
                p := orderedPair(members[i], members[j])
                if !seenPairs[p] {
                    a := columnRefByIdent[members[i]]
                    b := columnRefByIdent[members[j]]
                    added = append(added, &BinaryOp{
                        Op:    parser.OpEq,
                        Left:  a,
                        Right: b,
                    })
                    seenPairs[p] = true
                }
            }
        }
    }
    return added
}

// isColumnRefEquality returns the (left, right) ColumnRefs
// when expr is `ColRef = ColRef`, else (_, false).
func isColumnRefEquality(e Expr) (struct{ left, right *ColumnRef }, bool) {
    bo, ok := e.(*BinaryOp)
    if !ok || bo.Op != parser.OpEq {
        return struct{ left, right *ColumnRef }{}, false
    }
    l, lok := bo.Left.(*ColumnRef)
    r, rok := bo.Right.(*ColumnRef)
    if !lok || !rok {
        return struct{ left, right *ColumnRef }{}, false
    }
    if l.Type != r.Type {
        return struct{ left, right *ColumnRef }{}, false // type-aware
    }
    return struct{ left, right *ColumnRef }{l, r}, true
}

func identOf(c *ColumnRef) columnIdent {
    return columnIdent{
        name:           c.Name,
        sourceTableIdx: c.SourceTableIdx, // M0071-0009
        schemaPos:      c.Index,
        typeName:       c.Type, // optional
    }
}

func orderedPair(a, b columnIdent) [2]columnIdent {
    if a.name < b.name ||
        (a.name == b.name && a.sourceTableIdx < b.sourceTableIdx) {
        return [2]columnIdent{a, b}
    }
    return [2]columnIdent{b, a}
}
```

### Hookup in planner.go::Plan

```go
// Existing flow:
// conjuncts := splitConjuncts(query.Where)
// conjuncts = pushPredicatesIntoCrossJoins(...)
// plan := enumerateBushyPlans(graph, conjuncts, cat)

// M0075-0001 addition:
synthesised := inferTransitiveEqualities(conjuncts)
conjuncts = append(conjuncts, synthesised...)

// Then continue with existing pushdown / DP enumeration.
```

The synthesised conjuncts feed both filter pushdown
(re-running `pushPredicatesIntoCrossJoins` after
synthesis would let the new predicates push through
relevant joins) AND join-order DP (via additional
edges in the join graph).

### Re-run pushdown after synthesis

To maximise the win, the order should be:
1. `pushPredicatesIntoCrossJoins(initial conjuncts)` —
   first pushdown pass.
2. `inferTransitiveEqualities(updated conjuncts)` —
   synthesise closure.
3. `pushPredicatesIntoCrossJoins(updated conjuncts +
   synthesised)` — second pushdown pass.
4. `enumerateBushyPlans(graph, conjuncts, cat)`.

Or simpler: synthesise first, then run pushdown once.
The first option is more thorough; the second is
simpler. Recommend the simpler form for the initial
commit; if Q5 doesn't compress, escalate.

## Verification

Pre-commit gate (M0075 standard):
- `./tpch-runner --queries=5 --explain` before/after
  diff — synthesised `c.nationkey = n.nationkey`
  predicate visible in Q5's plan tree.
- Q5 wall time: best-effort target < 60 s; hard floor
  ≤ M0074-final (1100 s cancel acceptable; regression
  past that triggers revert).
- 21-q SF=1 sweep: zero row-count change.
- Q1 / Q3 / Q11 / Q14 / Q16 wall time ≤ 110 % of
  M0074-final on stable queries (no pessimisation
  from synthesised predicates).

New tests in `internal/planner/equiv_class_test.go`:
- `TestEquivClassClosureSimple` — `a=b ∧ b=c` gains
  `a=c` exactly once.
- `TestEquivClassNoSpurious` — pre-existing `a=c` is
  not duplicated.
- `TestEquivClassMultiHop` — `a=b ∧ b=c ∧ c=d` gains
  `a=c`, `a=d`, `b=d`.
- `TestEquivClassRespectsTypes` — does NOT merge
  classes when types differ.
- `TestEquivClassRespectsSourceTableIdx` —
  same-name columns with different SourceTableIdx
  stay in different classes.
- `TestEquivClassNoSelfPair` — `a=a` doesn't synthesise
  anything.
- `TestEquivClassEmptyConjuncts` — empty input → empty
  output.
- `TestEquivClassNoNonEquality` — `a < b` doesn't trigger
  union.
- `TestEquivClassQ5Shape` — synthetic Q5-like
  conjuncts produce the expected `c.nationkey =
  n.nationkey` predicate.

## Risks

| # | Risk | Mitigation |
|---|------|-----------|
| R1 | Synthesised predicates inflate cost estimates → regress Q1/Q3 wall time | 21-q wall-time check in pre-commit gate; cost-model invariant test. |
| R2 | Closure computation creates a cycle / loops | Union-find converges in O(N α(N)); pin via TestEquivClassMultiHop. |
| R3 | Type-mismatch false-merge (e.g., int4 = int8 implicit cast) | `isColumnRefEquality` requires `l.Type == r.Type`; pin via TestEquivClassRespectsTypes. |
| R4 | Self-join columns (Q9 lineitem self-join) get spuriously merged | columnIdent includes sourceTableIdx; pin via TestEquivClassRespectsSourceTableIdx. |
| R5 | Synthesised predicates duplicate an existing one (e.g., already in seenPairs but with operands swapped) | orderedPair canonicalises; pin via TestEquivClassNoSpurious. |
| R6 | Q5 plan changes break TPC-H result expectations | 21-q row-count parity gate. |

## Migration plan

Single commit (Commit E in M0075):
1. Land `equiv_class.go` (data structure + pass).
2. Hook into `planner.go::Plan` after pushdown.
3. Land tests.
4. Run `./tpch-runner --queries=5 --explain` and
   capture before/after plan diff in commit message.
5. Verify gate: SF=1 sweep parity + Q5 EXPLAIN diff
   shows synthesised predicate.

## References

- `internal/planner/planner.go::Plan` — call site.
- `internal/planner/pushdown.go:31` —
  `pushPredicatesIntoCrossJoins` (hook before).
- `internal/planner/bushy.go:344` —
  `enumerateBushyPlans` (hook after).
- `internal/planner/bushy.go:1610` —
  `findColumnIndexByNameAndSource` (M0071-0009;
  not directly used but identical SourceTableIdx
  awareness).
- `docs/design/0006-0001-sampling-and-mcv-histograms.md`
  — selectivity infrastructure (synthesised
  predicates feed into selectivity calculation
  unchanged).
