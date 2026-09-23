Task: M0141-S2b-4 (UNION distinct parity) — design slice landed c9a2c1c50;
next is impl slice M0141-S2b-4a (flatten same-kind UNION chains).
Files: docs/design/0100-0149/m0141-s2b-4-union-distinct-decomposition.md
(the plan). Code to read first: internal/optimizer/windowsetoppaths.go
createSetOpPaths/addSetOpPaths (binary SetOp only; streaming arm = UNION ALL,
hashed arm = HashSetOp for UNION), and where the SetOp tree is built from the
parser's set-operation node.
Key symbols: PG plan_union_children (prepunion.c:1269) fold rule;
generate_union_paths (prepunion.c:676) candidate set; goopg SetOp,
setOpStreams, costSetOp.
Findings: only Q49 and Q75 plan a distinct UNION (TPC-DS SF0.25). PG: Q49
Unique -> Sort -> Append(3); Q75 Unique -> Merge Append over Gather Merge
children. goopg: nested binary HashSetOp Union on both pipelines.
Next step: read how goopg lowers `a UNION b UNION c` into SetOp nodes, then
design S2b-4a's representation (distinct step over a left-deep UNION ALL
chain, EXPLAIN rendered as one n-ary Append) and check EXPLAIN's SetOp
rendering path.
Gates run: pgbench smoke (docs-only commit), ralph-state-guard.
In-flight: none.
Owner calls pending: M0145-0008 (multiplier; executor legacy pin),
M0145-0012 (rows<=1 CTE arm), M0141-S2b-17a (G4 repin of Q16/Q95 ea keys).
