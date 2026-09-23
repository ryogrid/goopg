Task: M0141-S2b-4 (UNION distinct parity) — 4a ded1b8db3 and 4b f311b4b1a
landed. Open: M0141-S2b-4c (Merge Append -> Unique when every child is sorted
on the union pathkeys; witness Q75: PG plans Unique -> Merge Append over
Gather Merge children, goopg plans Unique -> Gather -> Parallel Append);
M0141-S2b-4d (hashed Distinct HashAggregate label + DISTINCT election; needs
DISTINCT candidates carried to the ordered rel; Q41 floor match at stake).
Findings: goopg has NO Merge Append path or executor (grep found only a
comment in windowsetoppaths.go), so 4c needs a Merge Append node + executor
operator + path; scope it as a design slice first.
Files: planner.go applySetOp, windowsetoppaths.go createUnionDistinctPaths /
addPartialSetOpPath, plan.go SetOp.UnionDistinctInput, distinctpaths.go.
Next step: 4c design slice — PG create_merge_append_path (pathnode.c) +
nodeMergeAppend.c (heap merge of sorted children); check how goopg's
GatherMerge executor merges sorted streams (reuse its heap merge).
Gates run (4b): units, regress union/select_distinct, spotcheck, sf025,
acceptance arm, fire-set — PASS (run after the nightly finished).
In-flight: none. Lesson: never run disk-heavy FORCE=1 gates during the
nightly (it failed the nightly's tpcds stage; filed AI-20260924-005446-001).
Owner calls pending: M0145-0008, M0145-0012, M0141-S2b-17a (G4 repin).
