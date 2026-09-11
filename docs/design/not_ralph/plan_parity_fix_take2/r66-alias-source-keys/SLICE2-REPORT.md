# R66 SLICE-2 REPORT — families G/T: the key chase + boundary rule (2026-09-11)

SCOPE: `SLICE2-SCOPE.md` rev 2 (reviewed APPROVE-WITH-NOTES, all 3
blocking remediated; re-review APPROVE-WITH-NOTES, Gate-0 evidence
saved). Closes R66 — the last R65-ordered round before the (a)
Materialize re-triage.

## 1. Result: rendering {Q7,Q9,Q10,Q13} → {Q10}, P0–P5 all hold

Renderer-only cut (`operators_explain.go`, no planner/executor/costing
line) + pins (`explain_alias_source_keys_test.go`):

- `resolveKeySource` chase (Sort/Filter preserving hop; Project hop
  with `!IsolatedScope`, positional `Targets[j]`, table-0-descend vs
  base-stop vs computed-stop, child-output name match; Aggregate hop
  with GroupingSets decline, group recurse, positional Aggs synth via
  shared `synthAggCall`; depth cap 4; sublink guards).
- Boundary rule (two halves, both PG-adjudicated, see §3):
  `filterBlocksChase` (sublink-Filter descent decline) and
  `qualifierNamesCTE` (CTE-qualifier stop decline via the statement
  CTE-name set on `subPlanReg`), plus the table-0-operand decline in
  computed targets and synth args.
- Entries: Sort-above-agg (Arm-S guard → chase → Arm-S fallback),
  Sort-over-Project (re-anchored name guard), Group arm (first move,
  existing S18 wraps). 4 comment headers maintained
  (`childAggregateThroughFilters` verified-accurate, untouched).

## 2. Gate ledger (SCOPE §6; long arms all FOREGROUND)

1. `go test ./internal/executor/ ./internal/optimizer/` green (9 new
   pins: Q16-shape, count-Star, Star-HAVING, G-shape sourced,
   transitive `count(x)`, table-0-decline, sublink-Filter,
   CTE-qualifier, sublink guard; alias-no-op SUPERSEDED per Gate-0-A);
   `go vet` clean. R65's 4 pins pass.
2. **spotcheck: DEFERRED, owned (third round running — re-run at the
   next planner-touching round).** `:65433` peer-held throughout;
   renderer-only diff, values byte-identical on the clone pair.
3. **Values** TPC-H 24/24 MATCH at every binary step (Slice-1→chase→
   boundary-rule); digests byte-identical modulo elapsed. P2 holds.
4. **Plans** TPC-H A/B: exactly 5 queries move (Q7/Q8/Q9/Q13/Q22),
   Sort Key + Group Key lines only — Q7/Q9/Q13 PG-identical,
   Q8/Q22 PG-faithful modulo strategy-driven parens (HashAggregate
   vs GroupAggregate-over-Sort) and N5 casts. DS plans-channel
   (whole-file SequenceMatcher diff, §3.5): Q21/Q39/Q91 sourced
   moves kept; Q49 reverted by the table-0 rule; Q44/Q54 reverted by
   the boundary rule; Q51's structure-faithful CASE kept. Zero
   structural moves anywhere. P3 holds with per-line PG adjudication
   (§3).
5. **pp**: PRE 6/14/0/2 rendering=4 → POST **6/14/0/2 rendering=1**
   on fixtures AND fresh live-PG captures (re-captured per round;
   Slice-1's Q8 PG-drift flipped back — reference oscillates,
   rendering verdicts identical on all refs). Per-query categories
   differ by EXACTLY three cells (Q7/Q9/Q13 shed `rendering`).
   Q6/Q11 stay MATCH. P0/P1/P5 hold.
6. **DS SF0.25 sweep** foreground, private binary, engine-sha
   fingerprinted: **PASS=96 MISMATCH=0 CKMISMATCH=0 ERROR=0 TIMEOUT=0
   SKIP=3**. P4 holds.
7. **plan-gate: explicit opt-out (R65 precedent).** 20/22 DIFFER on
   BOTH binaries, verdict sets byte-identical; full-file pre/post
   diff confined to the §2.4 key lines. 100% pre-existing staleness.

## 3. Deviations and findings (ledgered)

1. **Gate-0/0b (oracle, TEMP tables on `:65432`, zero persistent
   mutation; transcripts `/tmp/pp2/r66/gate0/`).** Flattened subquery
   GROUP BY alias → PG base text both lines (chase direction
   confirmed); `MATERIALIZED` CTE → `s.supp` (boundary stop
   confirmed); join+subquery alias → `r66j1.n_name` (multi-RTE
   qualification confirmed). N4 qualifier remainder ledgered (PG
   qualifies single-RTE upper refs; goopg bare — qualifier gaps were
   already N4-forgiven, R65 SCOPE §2).
2. **Table-0-operand fail-closed (mid-round, PG-adjudicated on
   `:65438`).** First DS A/B showed Q49 `return_ratio` →
   `((sum / sum))` vs PG's `in_web.return_ratio` (CTE boundary PG
   keeps; goopg inlines per K31) and Q51 aliases → a bare-ref CASE
   vs PG's base-qualified CASE. Rule: computed targets and synth
   args containing table-0 refs decline (alias nearer than
   half-chased internals). Measured: Q49 reverted; Q51 keeps its
   structure-faithful CASE (exists in goopg's tree; PG's refs differ
   structurally — ref-qualification gap recorded, not owned);
   TPC-H moves unchanged (all computed targets there take real-table
   operands — verified by identical A/B).
3. **Boundary rule (mid-round, PG-adjudicated).** The DS census
   initially MISSED Q44+Q54 (section-splitter blind spot — census by
   whole-file SequenceMatcher + raw grep counts from here on; R50-trap
   family): Q44 `rank_col`→`(avg(...))` vs PG `v1.rank_col`, Q54
   `c_customer_sk`→`my_customers.*` vs PG `customer.*`. Sort-anchored
   live dump placed both paths; PG keeps SubqueryScan v1 (GROUP
   BY+HAVING+InitPlan, unflattenable) and flattens my_customers (K31)
   respectively. Rules: (a) decline descending through a
   sublink-predicate Filter (Q44 reverted; R65 same-level synth +
   sublink-HAVING coexists — probed, intact); (b) decline
   stop-and-render at CTE-output qualifiers (statement CTE-name set
   on `subPlanReg`; consumer aliases fall through correctly since PG
   prints those too). Q44/Q54 reverted; everything else unchanged
   (whole-file diff). R10-§7: the away-moves never shipped — explained
   non-landings, and this time fixed pre-commit.
4. **Unit fixtures route around boundary Projects** (identity
   boundaryMap → Agg directly over join): search-boundary chase paths
   are live-gate-only; G-shape (subquery-created Project) pins cover
   the mechanism (the 2-table join pin was deleted rather than pin a
   divergence).
5. **Fingerprint exoneration.** An unattributed sweep into
   `ds-sweep-s2b/` resolved by engine-sha (`cbbebc17`) to MY OWN s2b
   binary — no contamination. Lesson kept: trust the fingerprint
   over timestamps, and re-verify census coverage after any anomaly.
6. **IsolatedScope=false on subquery Projects is unit-measured;**
   the runtime `!IsolatedScope` guard governs regardless. Future
   dumps should print the flag (review N1 kept).

## 4. Sequencing

After review: commit (explicit pathspec: code + pins + REPORT + TODO —
SCOPE/STEP0 committed) with `-n` + push → R66 CLOSED → the due (a)
Materialize re-triage as a new round with its own scope (R63's
insufficient-alone verdict re-measured on post-R66 numbers).

Evidence tmp-only `/tmp/pp2/r66/` (binaries `goopg-r66`/`goopg-r66s2b`/
`goopg-r66s2c` md5 `e8838463…`/`630c3966…`/`2db47afc…`, TPC-H arms +
captures + pp verdicts incl. fresh live-PG per round, `ds-sweep*/`
with engine fingerprints, `ds-pg/` live-`:65438` transcripts Q21/39/
44/49/51/54/91 + `dbg-*.sql`, `gate0/` oracle transcripts,
`plangate-*` pairs, server logs, `launch.sh`); pre-cut worktree
`/tmp/pp2/r66-pre-src` (HEAD); clones (servers DOWN, kept).
