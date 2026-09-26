# M0146-0015b recon evidence — nested EXISTS/NOT EXISTS pull into semi/anti joins

Canonical query (regress `subselect`, PG 4 ms vs goopg ~197 s at HEAD):

```sql
select a.thousand from tenk1 a, tenk1 b
where a.thousand = b.thousand
  and exists ( select 1 from tenk1 c where b.hundred = c.hundred
                   and not exists ( select 1 from tenk1 d
                                    where a.thousand = d.thousand ) );
```

Fixture: regress `tenk1` (`analysis/m0146/m0146-0015/seed.sql`), throwaway
cluster on :5533 at HEAD `12143918a` (post-M0146-0015c slices 1–4).

Files:

- `plan-pg183-canonical.txt` — PG 18.3 plan (copied from
  `m0146-0015/plan-pg183.txt`): `Nested Loop Semi Join` whose outer input is
  `Nested Loop[(Hash Anti Join a,d), Index Scan b]` — the inner NOT EXISTS
  is an ANTI JOIN inside the outer semi join's LEFT subtree, not a SubPlan.
- `plan-goopg-head-canonical.txt` — goopg at HEAD: `Merge Join` with
  `Filter: EXISTS(SubPlan 1)`; inner NOT EXISTS stays `SubPlan 2`
  (correlated `$0`, index probe — the M0146-0015a machinery works inside).
- `plan-goopg-head-cloneable-inner.txt` — same shape with `a.odd = d.even`
  (no index → d is a SeqScan, which `planCloneSupported` admits): the outer
  EXISTS DOES pull (`Nested Loop Semi Join b,c`) and the kept NOT EXISTS
  becomes slice-3's `NOT (ANY (odd = (hashed SubPlan 1).col1))` placed as a
  filter on the `a` scan. Correct rows; not PG's anti-join shape.
- `plan-goopg-head-exists-variant.txt` — plain inner EXISTS variant: outer
  pull succeeds too (`decline=(pulled)`) but the elected plan keeps
  `Nested Loop + Filter: EXISTS(SubPlan 1)` — the pulled admission doesn't
  force the semi join; the conjunct stays available as a residual.
- `census-goopg-head.txt` — `GOOPG_NLI_CENSUS=1` decline lines per shape.

## Findings

1. **The missing arm is PG's `jtlink1` insertion point for nested
   sublinks.** `pull_up_sublinks_qual_recurse`
   (`postgres/src/backend/optimizer/prep/prepjointree.c:749-754`) recurses a
   converted EXISTS's quals with TWO insertion links: `&j->larg` with
   `available_rels1` (the emitting spine) and `&j->rarg` with `child_rels`
   (the pulled body's rels). A nested sublink whose upper vars ⊆
   available_rels1 becomes a join inside `j->larg` — the anti join
   `(a,d)` here. goopg's `extractNestedPullups`
   (`internal/optimizer/jointreepullup.go:1256`) binds a nested body ONLY
   against the parent body context (`bodyCtx`) — the `j->rarg` arm. A body
   referencing only the emitting scope reads Level-2 in that scope chain
   and dies at `no-level1-correlation`
   (`jointreepullup.go:450`; `exprListHasOuterRefAtLevel(quals,1)`).
2. **PG's level accounting**: `convert_EXISTS_sublink_to_join`
   (`subselect.c:1547-1548`) runs `IncrementVarSublevelsUp(-1,1)` over the
   moved quals — INCLUDING inside nested SubLink subselects — so `a.thousand`
   inside `d`'s query drops from varlevelsup 2 to 1. The subset test at
   :1571 then admits `{a} ⊆ {a,b}` → `j->larg`. (`{c} ⊆ {c}` goes `j->rarg`;
   mixed/spanning fails both — PG's `nestedBodySpansScopes` equivalent.)
3. **goopg equivalent of the decrement**: bind the nested body with
   `parent = bodyCtx.parent` (the enclosing problem's resolve context)
   instead of `bodyCtx`. Emitting-scope refs then resolve as Level-1
   directly; refs to the parent body's rels (`c`) are unresolvable in that
   chain and the bind fails — exactly PG's `⊆ available_rels1` test.
4. **Second, orthogonal decline**: with the nested pull refused, the kept
   sublink goes through `keptSubplanAdmissible` → `planCloneSupported`
   (`unnest.go:4364`), which has no `BitmapHeapScan`/`BitmapIndexScan` arm →
   `nested-sublink-uncloneable` and the WHOLE outer pull aborts. Even after
   the larg arm lands, a genuinely-kept nested sublink (Level-1+Level-2
   spanning — un-pullable in PG too) with a bitmap plan still aborts the
   outer pull; widening the clone set is separate work.
5. **`hops > 1` (`nested-body-emitting-ref`) stays unreachable**: the larg
   binding makes the emitting scope Level-1, so `rebasePulledQual`'s
   existing hops==1 arm rebases it — no SpecialJoinInfo min-hand widening
   needed for this shape either.

## Impl sketch (for the filed child task)

`extractNestedPullups` gains a larg arm before today's bodyCtx arm (PG's
jtlink1-first order): try `pullUpExistsBody`/`pullUpAnyBody` with
`parent = bodyCtx.parent`; on success the child is a LEFT-scope body —
`parent = nil` semantics at `rebasePulledQual` (Level-1 = emitting),
leaves appended to the pulled band, ordered BEFORE the parent body in the
flat list (PG stacks larg inserts under the parent join), and the parent's
`sjLeft` widens by the larg child's leaves (`syn_lefthand` = the larg
subtree relids, prepjointree structure → make_special_join_info). On bind
failure (refs to the parent body's rels unresolvable) fall through to
today's `bodyCtx` arm (`j->rarg`). Depth≥2 larg bindings stay declined.
