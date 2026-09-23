Task: M0141-S2b-4 (UNION distinct parity) — 4a ded1b8db3, 4b f311b4b1a,
4c 5af350059 landed. Open: M0141-S2b-4d (hashed Distinct HashAggregate label +
DISTINCT election; needs DISTINCT candidates carried to the ordered rel; Q41
floor match at stake); M0141-S2b-4e (presorted branch paths for the Merge
Append arm; witness Q75: PG plans Unique -> Merge Append over Gather Merge
children, goopg still elects hashed).
Files (4c): windowsetoppaths.go createUnionDistinctPaths /
addUnionMergeAppendPath / unionAppendCost, plan.go SetOp.MergeKeys,
operators_setop.go nextMerge, operators_explain.go Merge Append arms.
Findings: add_path's 1% fuzz makes merge-vs-sort ties decisive — price both
candidates on the same basis (cost_append seed). Regress baseline method:
HEAD worktree at /tmp + symlinked postgres/, diff the +/- lines.
Next step: 4e — how does a leaf reach createUnionDistinctPaths (finished
Node)? Need the leaf's own sorted path (pathlist) to reuse Gather Merge /
index order; check applySetOp for where leaves are planned.
Gates run (4c): units, regress union/select_distinct, spotcheck, sf025,
acceptance arm, fire-set — PASS; ea-ratchet FAIL only on pre-existing
Q16/Q95 (owner G4 repin).
In-flight: none. Commit-msg hook: never name another task id (e.g. the
repin task) in a code commit message — it binds the commit to that task.
Owner calls pending: M0145-0008, M0145-0012, M0141-S2b-17a (G4 repin).
