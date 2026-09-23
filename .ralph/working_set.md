Task: M0141-S2b-4 (UNION distinct parity) — 4a/4b/4c landed; 4e marked [!]
this loop (blocked on M0145-0008: single-table ORDER BY gets no ordered index
path on the default arm, ledger c07-single-rel-never-reaches-ordered-index-
producer). No code landed for 4e (the ordered re-plan was inert; reverted).
Open: M0141-S2b-4d (hashed Distinct HashAggregate label + DISTINCT election;
Q41 floor match at stake). Q75's remaining UNION gap is exactly that label:
PG plans HashAggregate -> Gather -> Parallel Append, goopg Unique -> Gather ->
Parallel Append (the design doc's Merge Append witness for Q75 was wrong).
Files: docs design m0141-s2b-4-union-distinct-decomposition.md (corrected
Witnesses + S2b-4e section), README row, fix_plan, ledger.
Findings: goopg EXPLAIN qualifies Sort Keys inconsistently across set-op
branches (pre-existing rendering class, e.g. INTERSECT `Sort Key: ss_item_sk`).
Next step: re-read the banner; if M0141 is still the selected item, take 4d
(its prerequisite: carry DISTINCT candidates to the ordered rel).
Gates run: units (4e WIP, reverted), regress union/select_distinct — no
change vs 4c. Doc-only commit.
In-flight: none. The goopg sf025 bench server on :65437 was started this loop
(server.sh start sf025) for EXPLAIN probes and left running.
Owner calls pending: M0145-0008, M0145-0012, M0141-S2b-17a (G4 repin).
