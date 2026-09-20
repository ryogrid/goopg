Task: M0144-0011 — first vertical-slice campaign (recon; OPEN, children filed)
Files: analysis/m0144/m0144-0011-q8-slice-trace.md, m0144-0011-q8-dppath.txt
  (evidence), docs/design/0100-0149/m0144-0011-q8-vertical-slice.md,
  docs/design/README.md, .ralph/fix_plan.md
Key symbols: inputNodePathkeys (upperorderedinput.go:176 — no *Aggregate case),
  electOrderedGrouping (upperorderedgrouping.go:177 — cands<2 gate),
  createOrderedPaths (upperordered.go:64), groupingEmissionPathkeys (:78)
Hypothesis/Findings: Q8 slice traced end-to-end on private :5595 lane
  (tmp/c20a/data-sf025, binary ae57cbc7, HEAD 30e8ea711). Layer 2 gap =
  seed ordering claim: winning PathAgg carries pathkeys=1 but nothing
  transfers it to the ordered-step seed (keys=0 → redundant Sort).
  electOrderedGrouping cands<2 is a second divergence vs PG's
  iterate-all-pathlist. Deeper: NL 19852 dominated vs HJ 19455 at rel
  {0,1,2,3} (cost-input class); Materialize MISSING-NODE. Lane stopped.
Next step: banner order → M0144-0011a (ordering-claim propagation at
  ORDERED-step boundary) is the natural first child; verify banner still
  ranks M0144 work next loop.
Gates run: none yet (docs/recon only); ralph-state-guard pending this loop.
In-flight: none — private :5595 server stopped cleanly via goopg stop.
