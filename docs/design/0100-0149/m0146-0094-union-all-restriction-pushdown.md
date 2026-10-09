# M0146-0094 — restrictions pushed into UNION ALL appendrel members

Status: done 2026-10-08 (a578e5eb3). Parent: M0146-0066 (the recon's
incidental qual-placement finding). Evidence: `analysis/m0146/m0146-0094/`.

## Problem

A WHERE conjunct that reads only a FROM-clause UNION ALL leaf, such as
`… (select v, k, 1 as src from s1 union all select v, k, 2 from s2) u …
where src > 0`, stayed as `Filter: (…)` on the Append in goopg, whatever
it read. The recon filed only the member-constant case. The probe showed
goopg never pushed any restriction into the members.

## PG behaviour

- The leaf is an appendrel (`pull_up_simple_union_all`), and the conjunct is
  a baserestrictinfo of the appendrel parent.
- `set_append_rel_size` (allpaths.c) gives every child its own copy with the
  parent's columns replaced by the member's expressions
  (`adjust_appendrel_attrs`), passed through `eval_const_expressions`:
  - a copy that folds to TRUE disappears;
  - FALSE or NULL makes the child a dummy rel that leaves the Append
    (`set_dummy_rel_pathlist`);
  - an Append left with one child is no Append at all.
- The appendrel exists only when every member's tlist types equal the
  union's (`tlist_same_datatypes`), so a member-local copy compares under
  the union's types.

| query on `u` | PG 18.3 |
|---|---|
| `src > 0` | bare Append |
| `src = 1` | member 2 gone, no Append |
| `price > 5` | a `Filter: (v > 5)` on each member scan |
| `src = 2 and price > 5` | `Seq Scan on s2` with the filter |
| `tag <> 'a'` (`'a'::text` member) | member 1 gone |

## Change

`pushWhereQualsIntoUnionAllItems` (`subquerypushqual_unionall.go`) runs on
the AST before the FROM list is planned, beside
`pushWhereQualsIntoGroupedItems`, so the member plans and every estimate
above them see the filtered members.

1. **Items.** It reuses the grouped-item pass's FROM enumeration and column
   resolution (`newPushQualItem`, `resolvePushQualColumn`), with the
   union's output names as the exposed names.
   - Items on a nullable side, under RIGHT/FULL/NATURAL/USING, or LATERAL
     are declined.
2. **Conjuncts.** Every column reference must resolve to the one union
   item.
   - Sublinks, window and aggregate calls, and calls that are not known
     non-volatile built-ins decline the conjunct (`pullupSafeTargetCall`).
3. **Members.** The pass accepts a flat chain only; a parenthesised compound
   declines.
   - `*` and `alias.*` are expanded over plain FROM tables from the catalog,
     skipping dropped columns; NATURAL and USING decline.
   - Only plain members take pushes (`unionMemberPushSafe`). A grouped,
     DISTINCT, LIMITed, windowed, WITH or locking member, or a volatile,
     aggregate, window or set-returning call in any member's targets,
     keeps the union's quals on the Append. In PG those members are
     subquery RTEs under `subquery_push_qual`'s rules: a qual on an
     aggregate output is no WHERE qual, and one pushed below a LIMIT
     selects other rows.
4. **tlist_same_datatypes** (`unionColumnTypesAgree`). Each referenced
   union column must have one type signature across members:
   - literal kinds, normalised cast types, and catalog column types through
     the member's FROM tables;
   - a string literal beside text counts as text;
   - otherwise the conjunct stays. For example, `char(5) UNION ALL text` is
     bpchar, where `'x  ' = 'x'`.
5. **Substitution.** `substituteParserColumns` copies the conjunct with
   each column replaced by the member's target expression. It is a
   reflective copy that never enters a sublink and never mutates the
   original AST.
6. **Folding.** `foldParserConstBool` settles:
   - numeric comparisons and string `=`/`<>`;
   - booleans, NULL and IS [NOT] NULL;
   - AND/OR/NOT with three-valued logic.

   It looks through a cast that cannot change a literal (string to
   text/varchar, a number to an integer or numeric type, no typmod).
   - TRUE: nothing is added.
   - FALSE or NULL: the member is removed.
   - Otherwise: the copy is ANDed into the member's WHERE.
7. **Rebuild.** The chain is rebuilt from struct copies.
   - When the head is removed, the next member takes the union's column
     names as target aliases, over its `*`-expanded list.
   - A push that would remove every member is not made; PG's all-dummy
     appendrel is a shape this pass does not build.
8. **is_safe_append_member** is decided before PG pushes anything. The
   member's original WHERE is recorded on the statement's `rtableScope`
   (`appendMemberOrigWhere`), and `isSafeAppendMember` (M0146-0093) reads
   it, so a pushed qual never earns a member a `"*SELECT* n"` wrapper.

## Verification

- **Probes.** All five probe shapes match PG 18.3
  (`probe-union-restrictions.*`). The `src > 0` query differs only in
  parallelism: goopg elects a parallel plan for that union with or without
  the qual, which is a pre-existing election difference.
- **`TestUnionAllRestrictionPushdown`.** Covers five pushed shapes and
  three declines: mixed member types, a grouped member, and a join qual.
- **`TestUnionAllRestrictionPushdownValues`.** Compares pushed queries
  with the same queries fenced by `OFFSET 0`, which keeps them out of the
  pass.
  - Cases: constants, mixed quals, NULL tests, OR, an all-false qual, a
    LIMITed member, and `*` members.
  - Inverting the fold fails 4 cases.
  - Disabling the plain-member guard returns 1200 rows where the fenced
    query returns 1190.
- **Regress A/B.**
  - `union`'s diff drops from 352 to 343 lines, removals only. The
    constraint-exclusion case now prints PG's `Seq Scan on tenk1 b`.
  - subselect, with, inherit, partition_prune and select_parallel are
    identical.
- **TPC-DS.** No query changes; the corpus restricts no UNION ALL leaf.
- **Gates.** units, TPC-H spotcheck, acceptance arm, fire set, SF0.25 sweep
  (99/99 shapes unchanged) and ea-ratchet all PASS.

## Deferred (ledgered)

- **Non-plain members.** PG pushes into those through `subquery_push_qual`:
  grouping columns into HAVING, DISTINCT-safe columns. regress union's
  `select distinct * from int8_tbl … where q2 = q2` is the witness; PG also
  rewrites `q2 = q2` to `q2 IS NOT NULL`.
- **Parenthesised compound members** and pushes that would leave no
  member (PG's dummy appendrel).
- **Folding beyond literals.** PG folds any immutable expression
  (`eval_const_expressions`); this pass folds literal comparisons only and
  pushes everything else as a member qual.
- **Inlined-CTE UNION ALL appendrels** (M0146-0065) are not covered.
