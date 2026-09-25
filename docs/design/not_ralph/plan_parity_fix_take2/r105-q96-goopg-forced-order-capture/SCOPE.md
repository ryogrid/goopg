# R105 SCOPE — repeatable Goopg capture of R101's forced Q96 orders

R104 (`dc91bd6b7`) closes the semantic blocker that stopped R101: both
unchanged forced Q96 forms now execute and return 266 on Goopg. R101's valid
native PG18.3 capture prices hdem-first 176.16 below store-first. Before any
cost work, capture an equally reproducible Goopg baseline for exactly those
two forced forms; this is measurement only.

## Authorized measurement

Use the exact R101 SQL files and record their SHA-256 digests without editing
them. Build the current committed Goopg source into a private `/tmp` binary,
run it against an isolated SF0.25 clone on a private port, and enable only the
same opt-in path controls used by the post-R104 R101 rerun. Do not modify the
source dataset or run ANALYZE.

For each hdem-first and store-first form, capture text `EXPLAIN` twice and
assert byte-identical output, then execute it once and assert scalar value
266. Record the complete SQL paths and digests, source commit and build
identity, data clone and port, controls, complete plan text or a durable
digest plus full operator spine, root cost, both forced hash joins' child
rows/costs and output, cost margin, intended first-two leaves, and final
parameterized `time_dim` probe. Compare only the two forced forms; neither
form is evidence of natural join-order election.

## Boundaries

No production source, planner cost/selectivity, join enumeration/election,
executor, GUC/default, benchmark SQL, `postgres/`, reference-cluster, or data
change is authorized. Do not use hints or disable join methods. Stop the
private service after collection. Before measurement: agent review,
correction if needed, `git commit -n`, and push. Afterwards commit/push an
English report and TODO update separately from unrelated work.
