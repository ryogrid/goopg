# R107 SCOPE — establish a common-data Q96 forced-order oracle

R106 proves that R101 PG and R105 Goopg cost inputs are not comparable: the
observed `store_sales` estimates are 232218 versus 719876 and their dimension
estimates also differ. A scalar result of 266 is insufficient to attribute a
planner margin. Establish disposable, common-input Q96 evidence before any
cost diagnostic or change.

## Authorized measurement

Locate the exact SF0.25 text generation/load provenance usable by both
engines. Create fresh, private, disposable PG18.3 and Goopg clusters from the
same generated relation files and schema/index definitions, on distinct
private ports and paths. Record generator/version, every input file SHA-256,
schema/index SQL digest, load command, cluster/binary identities, and all
planning settings. Neither existing R100 nor R105 data directory may be
modified.

Run ANALYZE only in each new disposable cluster, and record every
participating Q96 relation's row count and every Q96 predicate/join column's
native statistics needed for the two forced forms. Before ANALYZE, record
per-engine post-load row-count and schema/index witnesses. Prove relation
equivalence with the identical generated files plus deterministic per-table
ordered checksums or an equivalent complete relation witness. Record rather
than equate engine-native statistics, including each engine/version's GUC and
sampling controls.
Capture both immutable R101 forms twice with text and JSON/structured EXPLAIN
where supported, plus values. Assert form-specific repeatability, values 266,
intended two Hash Join leaves, and the final parameterized `time_dim` probe.
If relation/schema/index equivalence fails, or either engine's required native
statistics cannot be collected and recorded, stop and report the exact
divergence; do not compare cost margins.

## Boundaries

No production planner/executor/source change, cost/selectivity change, join
enumeration/election change, benchmark SQL change, `postgres/` edit, existing
oracle mutation, or hint/disabled-join-method experiment is authorized. New
cluster initialization and loading are limited to the disposable paths named
in the report. Stop both services after collection. Before measurement: agent
review, correction if needed, `git commit -n`, and push. Afterwards commit/push
an English report and TODO update separately from unrelated work.
