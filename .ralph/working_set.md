Task: M0146-0195 — TPC-H fk-index probes (Q3/Q7 join-method, Q5 scan-type). [ ] — root cause established.
  - 7b356ec04 (landed): insert split no longer leaks pages — the bloat's cause.
  - 1781cc20b: CORRECTED measurement (previous "refuted" was invalid): with compact fk indexes actually used,
    TPC-H match 12 -> 14 (Q3/Q5/Q7 MATCH; Q10 lost because goopg bulk-built indexes are denser than PG's).
    Per-probe gap reproduced exactly: genericcostestimate's loop-amortised index-page term (PG 4121 pages -> 0.55,
    goopg 28617 -> 2.63 per probe). Evidence analysis/m0146/m0146-0195/ (m0195-s2-* arms).
  - Filed M0146-0197: goopg builds ONE parameterised index path per parameterisation by heuristic
    (pickIndexCoveringLeadingPrefix: most bound cols, ties -> name order); PG builds per index (build_index_paths).
  - Filed M0146-0196 (below item 10): REINDEX/DROP INDEX can't resolve relations in a CREATE DATABASE'd db.
BLOCKERS (owner) — resolved/dealt 2026-10-15:
  1. tpch-acceptance-arm: CLEARED — digestdiff gained an XFAIL baseline pin (EXPECTED-ERROR passes only the pinned
     error; XFAIL-RESOLVED forces a re-pin). baseline-digests.txt Q5 is XFAIL. The parked slice-2 arm output
     re-diffs to VERDICT: PASS. M0146-0178 de-escalated [ ]: resume = apply slice2.patch, re-run arm, commit.
  2. :65433 reload by INSERT with a 7b356ec04 binary: EXECUTED — NEGATIVE RESULT, fk indexes rebuilt to the SAME
     sizes (117050 / 28617 / 41512 pages). The dedup fix does not cover the real-corpus bloat path; compacting the
     ref indexes needs M0146-0196 fixed first (REINDEX/DROP INDEX resolve only DefaultDBOid — reproduced on :65433).
NEXT LOOP:
  1. Re-read banner; both blockers are cleared (see above) — the arm stamps again.
  2. M0146-0178 resume: the slice2.patch content is already staged in the index (loop applied it 2026-10-15
     evening) — re-run the arm, commit.
  3. Then M0146-0195's next step: cost_index term decompose (committable now that the arm passes) — and note the
     reload NEGATIVE result above: the fk-index bloat has a second driver beyond the dedup leak.
Files: analysis/m0146/m0146-0195/*, design m0146-0195 (Corrected measurement section), fix_plan, ledger.
Key symbols: pickIndexCoveringLeadingPrefix (nl_index_join.go), addOneParameterizedIndexPath (pathparamindex.go),
  btreeIndexAMCostPages / indexPagesFetched (costindex.go), IndexesOnTable (sorted by name).
Hypothesis/Findings:
  - scripts/tpch-estimate-audit-arm.sh PRE_AUDIT_SQL works for clone-side setup; name twins a_<orig> to make them
    win the heuristic tie, map back with sed for the diff.
Next step: check blockers; else decide between M0146-0197 (arm-blocked) and the bulk-build density finding (landable).
Top-residual: actionable join-method=4 (TPC-H Q2/Q3/Q7, SF0.25 Q43) — M0146-0195 owns Q3/Q7
Skip-rationale: n/a — next step stays on M0146-0195's family.
Gates run: no engine code changed (measurement + docs); pgbench smoke (hook).
In-flight: none.
Owner items: (DONE 2026-10-15: arm unblocked via XFAIL pin; :65433 INSERT reload executed — negative result on index
  bloat; M0146-0196 placed below item 10.) Still open: place the four S2 defects; XLogInsert/XLogFlush rename;
  M0122-0008; M0122-0009a/b/c, M0122-0010a and filed follow-ups; COSTTIE flips; plan-shaping GUC defaults.
