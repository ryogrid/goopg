Task: M0142-0008a-1 — design-only: read PG's `join_is_legal`/`SpecialJoinInfo`
construction machinery in full and produce a concrete goopg design for
wiring correlated EXISTS/NOT EXISTS decorrelation (semi/anti joins) into
the DP search's joinrel machinery. DONE and committed this loop.

Files: `docs/design/0100-0149/m0142-0008a-1-semi-anti-sji-design.md` (new).
`docs/design/README.md` (index entry, inserted before m0142-0008b).
`.ralph/fix_plan.md` (M0142-0008a-1 closed `[x]`; M0142-0008a-2/-3 rewritten
with the narrower/re-split scope the design found).

Key symbols read (no edits): PG oracle — `join_is_legal` (joinrels.c:350),
`make_outerjoininfo` (initsplan.c:1708), `pull_up_sublinks_qual_recurse`
(prepjointree.c:651). goopg — `SpecialJoinInfo`/`makeSpecialJoinInfoScoped`
(`internal/optimizer/specialjoin.go`), `deconstructJointreeScopedSJI`/
`collapse.go:514`, `joinsearchlevel.go`'s `joinIsLegal`/`joinInfoList`,
`unnestExistsExpr` (`unnest.go:4110`), `runJoinSearchBelowPinned`/
`whereEligibleForPreDPUnnest` (`predp.go`), `jointypeForDirection`/
`addPathsToJoinrel` (`joinpaths.go`), `addHashJoinPath` (`pathgen.go:78`).

Findings this loop (full detail in the design doc):
- **This is PG's own previously-deferred "S5b" work item**, not a fresh
  gap — `docs/design/correlated-subquery-planning/03-planner-decorrelation-extensions.md`
  §4.2 + `IMPLEMENTATION-TODO.md` R2-5 + deferral ledger row `csq-R2`
  (deferred 2026-07-21, reopen criterion: "a query differing from PG ONLY
  by semi/anti placement"). M0142-0008a's own census census is suggestive
  but NOT yet confirmed evidence the reopen criterion is met — a per-query
  plan-compare re-check is a prerequisite for -2/-3, not optional.
- **goopg already has PG's `SpecialJoinInfo`/`join_is_legal` fully ported
  and unit-tested for JOIN_SEMI/JOIN_ANTI**, wired end-to-end for ordinary
  `FROM ... JOIN ... ON` syntax (`collapse.go` → `deconstructJointreeScopedSJI`
  → `makeSpecialJoinInfoScoped`, consumed by `joinsearchlevel.go`). No new
  struct or legality algorithm is needed — corrects M0142-0008a-1's own
  filed framing ("design a data structure"), which assumed none existed.
- The real gap is four separable wiring holes (design doc §3): (a)
  `unnestExistsExpr` builds its Join{Semi,Anti} on the lowered Node tree,
  chronologically after `ctx.joinInfoList` was already snapshotted from
  `s.FromExprs` alone; (b) `runJoinSearchBelowPinned`'s descend only walks
  the pinned join's LEFT child, the RHS never becomes a search participant;
  (c) `RelSet` bits are assigned per-search-call from a local `bindings`
  ordering, not from whole-statement `SourceTableIdx` — a new SJI needs
  relids in that same per-call numbering; (d) **costliest**: `joinpaths.go`'s
  `addPathsToJoinrel` DECLINES to build a hash join for SEMI/ANTI at all in
  the generic DP path generator today (nested-loop only, deliberate,
  documented) — while `unnestExistsExpr` already builds HASH semi/anti
  joins directly for every census query with an equijoin pair. Wiring -3
  without first lifting this gate would regress Q4/Q21/Q22 + the TPC-DS
  channel-comparison queries from Hash to Nested-Loop-only.
- Re-scoped -2 (smaller than filed: attach an inert SJI, nothing consumes
  it yet) and split -3 into three increments (RHS-as-participant, legality
  wiring/integration verification, lifting the hash-decline gate — (iii)
  must land before/alongside (i)/(ii), not after). fix_plan.md carries the
  full per-increment text with file+line resume points.
- No deferral-ledger row (same precedent as M0142-0008a's own recon and
  M0140-0006's decomposition: a scoped decomposition into named, resumable
  sub-tasks is not a silent deferral).

Next step: per the `## Current Priority` banner (re-read first — it may
have moved), item 4's M0141/M0142 slices are still top. Newly-available:
**M0142-0008a-3(iii)** (confirm `createPlan`'s hash-join lowering is
generic over `Jointype: Semi/Anti` — a concrete, bounded trace-through
task) is probably the best next pick inside this family: it's small,
answers a blocking open question before -2/-3's real coding starts, and
doesn't require the §4.3 plan-compare re-confirmation gate first (that gate
blocks -2/-3's actual implementation, not this verification step). Also
still open and available: **M0141-S2b-2** (needs its own scoping pass, K24
precedent), **M0141-S2b-4** (has a witness, TPC-DS Q49, sized comparably to
S2b-1/S2b-5). M0142-0003i/-0003k(c) and M0141-S2b-6-resume remain BLOCKED
on the human-authorized shared-cluster-write decision (TPC-H `:65433`
reload).

Gates run: no `go build`/`go test` needed — `git status --short -- internal/`
confirmed empty before writing the status block (pure `docs/`+`.ralph/fix_plan.md`
change, PG-oracle reads under read-only `./postgres`, no server/cluster
started). `make ralph-state-guard` — found the same recurring clean-exit-
marker inconsistency as the last several loops (status="running"/
progress="completed" mismatch), auto-repaired to `progress="in_progress"`,
then passed.

In-flight: none.
