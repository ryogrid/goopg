# M0146-0112 — a dummy UNION ALL member is dropped from the Append

Status: done 2026-10-08 (4987641e3). Parent: M0146-0111.

## Problem

Since M0146-0111, a UNION ALL member with a constant-false WHERE plans as
a dummy Result. goopg still kept it under the Append:

```
select a from f1 where false union all select a from f2
goopg:  Append -> Result (One-Time Filter: false), Seq Scan on f2
PG:     Seq Scan on f2
```

## PG behaviour

- `set_append_rel_pathlist` and `generate_union_paths` skip every member
  whose rel is dummy (`is_dummy_rel`).
- An Append left with one member is elided by setrefs.
- With no member left, the set operation is itself a dummy rel, planned
  as a childless `Result  One-Time Filter: false`.
- A UNION (distinct) keeps the dummy member under its Append:
  `HashAggregate -> Append -> Result, Seq Scan`.

## Change

`pruneDummyUnionAllArms` (dummy\_setop.go) runs on the folded set
operation tree, right after `foldSetOpRange`.

- **Which links.** It rewrites the UNION ALL links that are not a distinct
  step's input.
- **Dropping members.** A dummy side is dropped, and a link with both
  sides dummy becomes one dummy Result. `isDummyRelNode` looks through
  Project and Subquery Scan wrappers to a childless Result whose one-time
  filter is a constant false.
- **Column names.** The surviving member keeps the link's output column
  names, which are the first member's, through a renaming Project that
  EXPLAIN never prints.

## Verification

- **Probe** (PG 18.3 vs goopg). These shapes match PG:
  - a dummy left member, a dummy right member, and both dummy;
  - a dummy middle member of three;
  - a dummy member inside a FROM subquery with an outer qual;
  - ORDER BY over the survivor;
  - result rows.

  Two differences remain, both unchanged from HEAD:
  - the UNION-distinct method (a cost election);
  - the survivor's Sort Key qualification. PG's range table still counts
    the pruned member, so PG qualifies; this falls under the already
    ledgered rtable-size rule.
- **Test.** `TestExplainDummyUnionAllMemberDropped`. Disabling the prune
  fails four plans; dropping the renaming Project fails the column-name
  check.
- **Regress A/B.** No change: no regress EXPLAIN has a dummy UNION ALL
  member, so the fixture is the witness.
- **TPC-DS / TPC-H.** No plan changes.
- **Gates.** units, TPC-H spotcheck, acceptance arm, fire set, SF0.25 sweep
  (96 PASS) and ea-ratchet all PASS.
