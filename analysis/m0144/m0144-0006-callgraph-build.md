# M0144-0006 — `debug_plan_callgraph`: planner-route traces

Instrument-3 capture. The scratch build now carries
`-finstrument-functions` on `src/backend/optimizer/` plus
`__cyg_profile_func_enter/exit` hooks (`optimizer/util/calltrace.c`)
emitting `CGT` records — every optimizer function entry/exit, named
offline via `nm`, interleaved with `CGT tag` arg markers. Design doc:
`docs/design/0100-0149/m0144-0006-callgraph-build.md`.

## Record vocabulary

```
CGT base <addr>              # PIE anchor: runtime &standard_planner (once/backend)
CGT e <depth> <this_fn> <call_site>   # function entry
CGT x <depth> <this_fn>               # function exit
CGT tag joinlist levels=N route=degenerate|hook|geqo|standard
CGT tag level=N prev=<relids ...>     # join_search_one_level, lower rels
CGT tag mkjoin jt=N joinrel=(b..) outer=(b..) inner=(b..)
CGT tag mkjoin illegal outer=(b..) inner=(b..)   # join_is_legal rejection
```

Resolution: `delta = base − nm(standard_planner)`; nearest
symbol-at-or-below after delta-correction. Local clones (`.part.0`,
`.isra`, `.constprop`) and GCC-local copies of extern leaf functions
resolve by the same rule.

## Captures (same 12-query set, Q23 per statement)

Raw `tmp/pg18-optdebug/calltrace/q*.log`; distilled skeletons
`analysis/m0144/optdebug-0006/q*-route.txt` via
`scripts/pg-calltrace-distill.py` (route-set filter + top-40 call counts).

| query | CGT entries | route lines | mkjoin tags | illegal | level tags |
|---|---|---|---|---|---|
| q3   | 11592  | 1132  | 4   | 0 | 2  |
| q5   | 34756  | 2524  | 13  | 0 | 7  |
| q7   | 66347  | 8082  | 32  | 0 | 4  |
| q10  | 33037  | 2824  | 21  | 4 | 8  |
| q12  | 12999  | 1142  | 4   | 0 | 2  |
| q18  | 295147 | 35266 | 128 | 0 | 6  |
| q23a | 48141  | 5220  | 33  | 0 | 11 |
| q23b | 92666  | 11256 | 83  | 0 | 13 |
| q34  | 20509  | 2026  | 13  | 0 | 4  |
| q40  | 86342  | 9454  | 32  | 0 | 4  |
| q42  | 11786  | 1170  | 4   | 0 | 2  |
| q47  | 38069  | 2874  | 18  | 0 | 5  |
| q56  | 103616 | 12744 | 75  | 0 | 12 |

All slices e/x-balanced; off-mode emits zero lines; Q7 keeps
`Limit→GroupAggregate→Gather Merge` under tracing.

## First reads

- **Route choice is explicit per joinlist**: 8 `route=degenerate`
  (single-rel subqueries), `route=standard` at levels 2,3,4,5,7 — no geqo,
  no hook. The DP-search route is directly comparable with goopg's
  joinsearch path.
- **`mkjoin illegal` exists in the wild** — Q10 emits 4:
  `outer=(b 1|b 1 2|b 1 3|b 1 2 3) inner=(b 5)` — `join_is_legal`
  rejecting (b 5) against every left-deep extension. Route-level evidence
  of SpecialJoinInfo legality constraining the DP enumeration — the
  PG-side answer to M0144-0003a's semi/anti admission question, now
  machine-readable instead of source-read.
- **Appendrel route confirmed per subplanning** — Q5/Q56 show
  `pull_up_simple_union_all` → `add_paths_to_append_rel` →
  `create_append_path` ×4 (one per CTE subquery planning), matching the
  M0144-0003b template and the 0004 survivor dumps.
- **`pull_up_subqueries` is the PG18 marker for sublink pullup**
  (`pull_up_sublinks` the internal helper it calls) — appears at every
  subquery_planner head.
- **Volume profile**: trivial 2-join EXPLAIN ≈ 5.8 k CGT events; Q7 ≈
  66 k; Q18 ≈ 295 k. ~10× PLANCAND volume for the same queries — the
  distilled skeletons (≈1–35 k lines) are the commit-able artefact.

## Resume points

- Raw slices + binary keep coexisting with the 0004/0005 instruments on
  `:5560` — one `server.log` slice can hold `CGT` + `PLANCAND` + pprint
  blocks simultaneously (all three GUCs/macro write stdout).
- The route trace answers "which path through the planner" for every
  census query — pair with `GOOPG_PGSHAPED_DP_TRACE`'s own route markers
  for the mechanical route-diff.
- Rebuild gotcha (design doc §gotchas): `.SECONDARY:` defeats
  flag-only rebuilds — name `.o` targets explicitly when recompiling.
