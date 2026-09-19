Task: M0144-0004 — instrumented PG 18.3 instrument 1: OPTIMIZER_DEBUG build + survivor-pathlist capture. DONE, committed this loop.

Files: scripts/pg-optdebug-survivors.py (new distiller); analysis/m0144/m0144-0004-optdebug-build.md; analysis/m0144/optdebug-0004/q{3,5,7,10,12,18,23,34,40,42,47,56}-{survivors,plan}.txt; docs/design/0100-0149/m0144-0004-optdebug-instrumented-pg.md; docs/design/README.md; docs/milestones/0144-*.md; .ralph/fix_plan.md (0004 [x]).

Key symbols: pprint(rel) at allpaths.c:562/:3523/:4416 + planner.c:1310; nodeToString field namespacing (path. / jpath.path. / bare); cheapest_total_path = node-valued field at list-less depth.

Hypothesis/Findings: instrumented build plan-shapes match :65438 on all 12 captured queries (ANALYZE re-sample drift only). APPENDPATH survivors incl. parallel partial arm captured in Q5/Q23/Q56 — the M0144-0003b template. Survivors-only limit recorded (0005's scope).

Next step: banner item 2 continues → M0144-0005 (debug_plan_candidates trace GUC — patch the SAME scratch tree tmp/pg18-optdebug/src, rebuild in place; data dir tmp/pg18-optdebug/data reusable as-is).

Gates run: ralph-state-guard OK; 12/12 plan-shape MATCH vs reference capture; distiller verified on all 12 slices.

In-flight: instrumented PG left RUNNING on :5560 (pg_ctl daemonized postmaster, private trust-auth lane, tmp/pg18-optdebug/data, log tmp/pg18-optdebug/server.log). Stop with: tmp/pg18-optdebug/install/bin/pg_ctl -D tmp/pg18-optdebug/data stop. Harmless to leave; 0005 can reuse it (same binaries once rebuilt) or initdb fresh.
