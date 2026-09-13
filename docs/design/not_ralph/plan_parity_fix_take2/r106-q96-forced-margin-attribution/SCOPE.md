# R106 SCOPE — attribute the forced Q96 margin before changing costs

R101 measures a valid PG18.3 forced hdem-first advantage of 176.16, while
R105 measures only 1.92 for the same two immutable forms on post-R104 Goopg.
Both forms return 266 and retain the same leaf order and final `time_dim`
probe. The root-margin mismatch is real measurement input, but does not name
its cause or authorize a planner change.

## Authorized measurement and audit

Use only R101's unchanged SQL digests, R101's retained native-PG JSON plan
captures, R105's repeatable Goopg text-plan hashes, and a new isolated Goopg
run with default-off `GOOPG_PGSHAPED_DP_TRACE=1` in addition to R105's path
controls. Run each forced form twice for trace repeatability and once for
value 266. Each trace-on run's complete `psql -X -A -t` EXPLAIN SHA-256 must
equal the corresponding R105 trace-off hash; record both comparison inputs
and result, as well as trace-on run-to-run equality. Retain a separate
immutable trace artifact for each form/run (or an equivalently delimited
server-log extraction) and record each artifact SHA-256, so no interleaved
statement log is attributed to the wrong form. Do not mutate either database
or run ANALYZE.

For each form's first-level and second-level Hash Join (four operators total),
build separately labelled form/level ledger rows for child scan cost,
estimated rows/width, join selectivity/output rows, hash-build/probe
components where exposed, final parameterized probe cost, and each operator's
incremental total-cost delta. Trace fields must be identified as observed,
inferred from printed plans, or unavailable; do not claim PG internal
component equality when JSON does not expose it.

Audit only the Goopg cost/selectivity code paths reached by those observed
operators. Connect every candidate term to a ledger field and state whether
the forced-margin difference can or cannot be isolated using available
evidence. Stop with an unobservable result if the trace cannot distinguish
candidate terms. Preserve complete reproducible command lines, binary/source
identities, data clone/port, environment, SQL/trace hashes, plan spines,
values, and service shutdown in the report.

## Boundaries

No production source, cost/selectivity constant or formula, path filing,
join enumeration/election, executor, GUC/default, benchmark SQL,
`postgres/`, reference-cluster, source-data, or index/statistics change is
authorized. Do not use hints or disable join methods. Forced forms remain
attribution instruments only, never natural-election proof. Before
measurement: agent review, correction if needed, `git commit -n`, and push.
Afterwards commit/push an English report and TODO update separately from
unrelated work.
