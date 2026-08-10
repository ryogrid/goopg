(idle — nothing in flight)

Last loop: M0119-0006 **22nd slice landed** — `arr[i]` now yields the ELEMENT
TYPE's Datum instead of text. Resumed uncommitted WIP found in the tree (the
baton said "none" — the previous loop was cut off after committing 46103e4e and
before rewriting this file; the WIP still had debug `println`s in it).

Findings worth carrying:

- The blind spot was ONE fact repeated at three sites: a user array column is
  `catalog.Type{Name:<ELEMENT>, IsArray:true}` — never `elem[]`, never `_elem`.
  Analyzer `analyzeExpr`, planner `exprType`, and (via a new `ReturnType` stamp)
  the executor all probed only for the spellings they never get.
- The expensive half was the FOURTH site: five `case *FuncCall:` clone helpers
  (`FoldConstants`, `remapColumnRefsToSchema`, `shiftColumnRefsBy`,
  `shiftExprColumnIdx`, `unnest.go`'s `rewriteIdx`) rebuild the node field by
  field and dropped `ReturnType`. Symptom was "stamp applied, executor sees
  empty" while `pg_typeof` was correct. This also silently un-typed USER-DEFINED
  function calls that survived folding/remap — worth remembering for any future
  plan-time stamp: check every clone site, not just the setter.
- `float8[]` was a wrong answer nobody had filed (`9.5 > 10.2` → `t`). goopg has
  no `KindFloat`; a float8 COLUMN is already `KindNumeric`, so the float arm
  makes the subscript agree with its own scalar column.
- Probe technique that paid off: a throwaway `internal/executor/zz_probe_test.go`
  on `newDDLFixture`/`runQuery` gives full SQL end-to-end in ~10 ms; the PG
  oracle on 65432 (`PGPASSWORD=postgres`) answers the same query set in one psql
  call.

Banner state (re-read this loop): M-NIGHTLY's six `20260810-011258` items all
filed AND checked; M0130 fully checked; banner falls through to M0119, then
M0122.

Next loop: continue M0119-0006. Fresh candidates: date/time array elements
(needs `KindTime` rendering proven byte-identical to `decodeArrayElem` first),
array SLICES `a[1:2]` (rejected by the LEXER — parser slice), the `ReturnType`
typmod widening. Older: `internal/wal/pgoutput.go` ignores `t.IsArray`;
`interval[]` refused by `decodeArrayKeyElemText`; posting-list duplicate
coverage in the checkunique tier; `box`/`int4range` key encodings; the
whole-database (unscoped) pg_amcheck run.

Gates: units PASS; `scripts/tpch-spotcheck.sh` PASS (Q12=2, Q13=35);
`TestPort_RegressSuite` PASS (315s — first attempt died at the 600s package
timeout while the nightly ci/batch was saturating the host; the re-run was
clean, so load flake, not a regression). TPC-DS SF0.5 sweep NOT run: the script
hard-refuses while ci/batch is live, and the changed lines are array-subscript
and `ReturnType`-carry only — TPC-DS has no array columns and no UDFs.

In-flight: none
