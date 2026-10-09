# M0146-0132 — a pulled-up UNION ALL member skips the parallel size cutoff

Status: done 2026-10-09 (f06a67a5c). Parent: M0146-0014a.

## Problem

TPC-DS Q5's `csr` branch reads
`(SELECT ... FROM catalog_sales UNION ALL SELECT ... FROM catalog_returns)`
as a FROM subquery.

- **PG** plans a Parallel Append over two partial scans, including
  `Parallel Seq Scan on catalog_returns (cost=0.00..933.15)`.
- **goopg** put `Seq Scan on catalog_returns (cost=0.00..1081.66)` under
  its Parallel Append as a non-partial member.

The SF0.25 parity diff showed `[join-order, join-method, scan-type,
parallelism, qual-placement]`.

## PG behaviour

`compute_parallel_worker` (allpaths.c) returns 0 below
`min_parallel_table_scan_size` / `min_parallel_index_scan_size` only when
`rel->reloptkind == RELOPT_BASEREL`:

> If the number of pages being scanned is insufficient to justify a
> parallel scan, just return zero ... unless it's an inheritance child. In
> that case, we want to generate a parallel path here anyway. It might not
> be worthwhile just for this relation, but when combined with all of its
> inheritance siblings it may well pay off.

`pull_up_simple_union_all` flattens a FROM subquery that is a simple
UNION ALL into an appendrel. A member is pulled up into the parent only
when it is a simple subquery whose jointree is one relation with no
quals (`is_simple_subquery && is_safe_append_member`). Its relation then
becomes `RELOPT_OTHER_MEMBER_REL`. Every other member stays a subquery,
and its relations are base rels of a subroot.

catalog_returns at SF0.25 is about 721 pages. That is below the 1024-page
(8 MB) cutoff, but the cutoff does not apply to it.

## Change

- **`costParams.otherMemberRel`** (cost_funcs.go). When set,
  `computeParallelWorker` (considerparallel.go) skips both cutoffs. The
  log3 ladder starts at one worker. The `parallel_workers` reloption and
  `max_parallel_workers_per_gather` still apply.
- **Deciding the flag before the member is planned.** The search needs it,
  and the plan-based `isSafeAppendMember` can only answer afterwards. So
  `isSafeAppendMember` is split, and its AST half becomes
  `isSafeAppendMemberStmt` (subqueryscan_appendmember.go): one FROM item,
  no WHERE, no grouping, DISTINCT, ORDER BY, LIMIT or locking.
  `planSelectImpl` evaluates it for an appendrel member scope
  (`plannerSet.appendrelMember`), together with "no aggregate stage, no
  window stage" (`is_simple_subquery`). The result is stored as
  `resolveContext.appendrelOtherMember`.
- **Threading.** The seam copies the flag into
  `joinlistProblem.otherMemberRel`. `searchOneProblem` sets it on that
  search's own `costParams` copy, and only when the statement's FROM is
  that one relation. The base-rel seq-scan producer and the partial
  index-scan producer of that search therefore both see it, and nothing
  else does.

`resolveContext.appendrelMember` had been stamped with nothing reading
it. The new flag sits beside it; the stamp runs before the join search
(the `tryJoinSearch` calls follow `planFromClause`).

## Verification

- **Test.** `TestSmallAppendrelMemberGetsPartialPath`: a 700-page member
  files a partial path, and the same member with its own WHERE does not.
  The test fails with the exemption disabled.
- **Trace.** On the SF0.25 clone, `catalog_returns` turns from
  `veto=B4 workers=0` to `admitted workers=1`, at PG's exact
  933.15. `web_returns` is a join member (`web_returns LEFT JOIN
  web_sales`), so PG keeps it a subquery and goopg still vetoes it.
- **TPC-DS fire set.**
  - Q5 at SF0.25 goes to `[join-order, join-method, scan-type]`, and its
    results are identical.
  - SF0.25 CATEGORIES-EXCL-MATCH: parallelism 20 → 19, qual-placement
    7 → 6.
  - SF1: no plan changed.
- **TPC-H.** Plans are byte-identical.
- **Regress A/B** (32 cases): only `stats_ext`'s nondeterministic
  statistics-object listing order changes.
- **Gates.** units, TPC-H spotcheck, acceptance arm (values), fire set,
  SF0.25 sweep (96/96) and ea-ratchet (1, unchanged) all PASS.

## Not covered (ledgered)

- **Q5's remaining difference** is the `wsr` branch's `web_site` join.
  PG hashes `"*SELECT* 2"` against a Seq Scan on `web_site`; goopg probes
  `web_site_pkey` from a nested loop.
- **SRF members.** A member whose target list holds a set-returning
  function fails `is_simple_subquery` in PG. goopg's pre-planning check
  does not look for SRFs, so such a member would be wrongly exempted.
  Upstream has no corpus witness.
- **Column-type mismatch.** A UNION ALL whose members disagree on column
  types is not flattened in PG (`is_simple_union_all`'s
  `tlist_same_datatypes`). goopg learns that only after planning the
  members, so they are still exempted.
