Task: M0129-S6 — resjunk-ctid column path re-enable (M0128-P6.1 durable fix)
Files:
- docs/design/0129-0003-resjunk-ctid-column-path.md (NEW — design doc, draft)
- docs/design/README.md (index updated)
- .ralph/fix_plan.md (status update next)

Key symbols:
- wireRowMarkCtidColumns (planner.go:1993) — disabled; adds ctid columns to scans + Project
- recomputeIntermediateSchemas — NEW function to add; post-order walk recomputing Join/NLIJ schemas
- numCtid (planner.go:1636) — set to 0 (disabled); replace with wireRowMarkCtidColumns call
- planSelect (planner.go:723) — the build sequence; lock block at :1612

Hypothesis/Findings:
- Column path is fully wired at executor level (seqScanOp emits ctid datums when schema extended,
  lockRowsOp reads via CtidResno). Only planner intermediate-node schema propagation missing.
- Chose approach (a) bottom-up recomputation over (b) scan-creation injection:
  (a) is minimal — only Join and NestedLoopIndexJoin store their own schema and need explicit
  recomputation; all other intermediate types delegate Output() to child and auto-correct.
- Design doc written; ready for implementation.

Next step:
  Implement: (1) write recomputeIntermediateSchemas function in planner.go,
  (2) replace numCtid := 0 with numCtid := wireRowMarkCtidColumns(out, locks),
  (3) call recomputeIntermediateSchemas(out) when numCtid > 0,
  (4) re-enable TestPlanCtidRowMarkWiring in locking_test.go, add self-join FOR UPDATE case,
  (5) verify TestPort_IsolationEvalPlanQual PASSes.

Gates run: none (design-only loop)
In-flight: none
