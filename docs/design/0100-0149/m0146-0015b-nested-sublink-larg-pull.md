# M0146-0015b: a nested sublink that reads only the emitting scope

Status: **landed 2026-09-26** (M0146-0015d, EXISTS arm only — see "Landed").
Recon evidence `analysis/m0146/m0146-0015b/`; impl evidence
`analysis/m0146/m0146-0015d/`.

## The case

Regress `subselect` query (`analysis/m0146/m0146-0015/query.sql`):

```sql
select a.thousand from tenk1 a, tenk1 b
where a.thousand = b.thousand
  and exists (select 1 from tenk1 c where b.hundred = c.hundred
              and not exists (select 1 from tenk1 d
                              where a.thousand = d.thousand));
```

PG 18.3 plans `Nested Loop Semi Join` over `Nested Loop[(Hash Anti Join
a,d), Index Scan b]` in 4 ms — the inner NOT EXISTS is a real anti join
inside the outer semi join's LEFT subtree. goopg at HEAD (`12143918a`,
post-0015a/0015c) keeps both sublinks as correlated SubPlans under a Merge
Join; ~197 s standalone.

## PG's mechanism (oracle trace)

`pull_up_sublinks_qual_recurse` hands a converted sublink's pulled quals
**two** insertion links (prepjointree.c:695-700, :749-754):

- `&j->larg` with `available_rels1` — the scope the conversion was
  offered (here the top-level spine `{a,b}`);
- `&j->rarg` with `child_rels` — the pulled body's own rels (`{c}`).

`convert_EXISTS_sublink_to_join` (subselect.c:1449+) separates the body's
WHERE, runs `IncrementVarSublevelsUp(-1, 1)` over it — including inside
nested SubLink subselects (subselect.c:1547-1548) — so a reference two
levels out drops to varlevelsup 1 once its intermediate query level is
consumed. `d`'s `a.thousand` reads level-1 at conversion; `upper_varnos =
{a}`; `bms_is_subset({a}, {a,b})` passes for the `j->larg` arm
(subselect.c:1571) and `d` converts to `JoinExpr{ANTI, larg: {a,b},
rarg: d}` inserted under `j->larg`. The `a × b` inner join then orders
freely inside that subtree — `min_lefthand` covers only the rels the join
needs (`make_special_join_info`), which is why the plan's anti join is
`(a,d)` alone and `b` joins above it.

The `j->rarg` arm is the symmetric second slot: a nested body whose upper
vars ⊆ `child_rels = {c}` inserts inside the pulled body's subtree. A body
referencing both scopes (`{a,c}`) fits neither and stays a SubPlan —
M0146-0015c's canonical kept-subplan shape.

## goopg's gap (measured at HEAD)

`extractNestedPullups` (jointreepullup.go:1256) offers a nested body only
the `j->rarg` arm — it binds `pullUpExistsBody`/`pullUpAnyBody` with
`parent = bodyCtx` (the enclosing body scope). In that scope chain `d`'s
`a.thousand` is Level-2 and `pullUpExistsBody` dies at
`exprListHasOuterRefAtLevel(quals, 1)` →
`no-level1-correlation` (jointreepullup.go:450). Census:

```
PULLUPCENSUS decline=no-level1-correlation      <- nested pull attempt
PULLUPCENSUS decline=nested-sublink-uncloneable <- outer pull aborts
```

The second line is the knock-on: the refused NOT EXISTS stays in `c`'s
quals and `keptSubplanAdmissible` must clone `d`'s already-planned body —
`planCloneSupported` (unnest.go:4364) has no `BitmapHeapScan` arm, so the
OUTER pull aborts too. With a cloneable inner plan (`a.odd = d.even`,
SeqScan) the outer pull does land: `Nested Loop Semi Join (b,c)` plus
slice-3's `NOT (ANY (odd = (hashed SubPlan 1).col1))` on the `a` scan —
correct rows, but the filter placement is goopg's, not PG's anti join.

## The fix shape (impl slice, filed child)

`extractNestedPullups` gains a larg arm, tried FIRST per PG's order
(jtlink1 before jtlink2):

1. Re-bind the nested body with `parent = bodyCtx.parent` — the enclosing
   problem's resolve context. Emitting-scope refs (`a`, `b`) resolve as
   Level-1; refs to the parent body's rels (`c`) are unresolvable and the
   bind fails — the binding itself performs PG's `⊆ available_rels1`
   subset test. On failure, fall through to the existing `bodyCtx` arm
   (`j->rarg`), unchanged.
2. A larg-bound child gets `parent = nil` semantics at `rebasePulledQual`
   — its Level-1 refs are emitting refs and take the existing `hops == 1`
   arm. `hops > 1` (`nested-body-emitting-ref`) stays a guard.
3. Ordering: the larg child splices into the SPINE below its parent's
   join — flat list order `larg-child, parent` — and the parent's
   `sjLeft` widens by the child's leaves (PG's syntactic `syn_lefthand`
   covers the whole larg subtree). Without that widening the search could
   legally place `(spine ⋈ c) ⋉̸ d`, a join order PG's jointree structure
   never produces.
4. Depth≥2 larg bindings stay declined (the enclosing scope arithmetic
   generalises but is unmeasured); the ANY arm (`pullUpAnyBody`) is the
   same one-line arm if the EXISTS arm proves out.

## Secondary boundary (ledgered)

`planCloneSupported` covers only SeqScan/IndexScan/Values/CTEScan-shaped
plans. A genuinely-kept nested sublink (Level-1+Level-2 spanning — PG
keeps it too) whose plan contains `BitmapHeapScan` still aborts the outer
pull via `nested-sublink-uncloneable`. Widening the clone set is
orthogonal work.

## Landed (M0146-0015d)

Implemented per the fix shape above, one deviation:

- `jtPulledBody` carries a separate `largChildren` list (the rarg
  `children` list is unchanged). `extractNestedPullups` returns both and
  tries the EXISTS bind against `bodyCtx.parent` first, falling back to
  `bodyCtx`.
- `flattenPulledBodies` emits a body's larg children immediately before
  the body itself, stamping them with the body's own `parent` so each
  body is represented exactly once; rarg descendants still follow the
  parent.
- `classifyPulledQuals` widens the parent body's `leftBits`/`sjLeft` by
  the larg children's subtree leaf ranges — the `syn_lefthand` analog.
- Canonical query on tenk1 now plans `Merge Join (a=b)` over
  `Nested Loop Anti Join (a,d)` in the left subtree and
  `Nested Loop Semi Join (b,c)` on the right — PG's larg structure
  (`analysis/m0146/m0146-0015d/canonical-explain.txt`). 0 rows in
  ~0.23 s vs ~197 s as correlated SubPlans.

**The ANY arm was tried and removed.** `outerOperandAsLevel1` rewrites a
pulled ANY operand's `ColumnRef`s into `OuterColumnRef{Level:1}` resolved
*by column name*. For a larg child those names re-resolve against the
emitting scope; TPC-DS Q83's operand (`d_week_seq`, bound in the middle
body's `date_dim`) silently landed on the emitting problem's own
`date_dim` leaf and `createPlan` panicked in `translateToLayout`
(binding column not among the re-based outputs). PG's
`IncrementVarSublevelsUp` re-levels by varno and cannot misresolve;
goopg's name-based operand binding makes the ANY larg arm unsound for
parent-scope operands, and emitting-scope operands arrive as
`OuterColumnRef` and are refused anyway — so the arm can never bind
correctly. `pullUpAnyBody` keeps the rarg path only; a regression test
pins it ("ANY operand bound in the parent scope never larg-binds").

## Expected movement

None on the parity instruments (the corpora carry no two-scope nested
sublink — M0146-0015c slice-3 census; confirmed: SF0.25 planset and the
TPC-H parity capture are byte-identical to the pre-change baselines). The
measurable artifact is the regress `subselect` plan shape (`Nested Loop
Semi Join` over `Hash Anti Join (a,d)`) and its runtime — verified by
live EXPLAIN on a seeded throwaway cluster plus the
`TestPort_RegressSuite` `subselect` case.
