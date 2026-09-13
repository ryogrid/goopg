# R111 SCOPE — retry Q96 common-data oracle after varchar fidelity repair

R107 stopped because a full `store` witness exposed Goopg varchar whitespace
loss. R110 (`19c6f4430`) repairs that loss, and its fresh reload's normalized
`store` witness now matches PG. Re-establish the complete common-input Q96
oracle before R108's PG hash-size comparison.

## Authorized measurement

Use only R107's immutable four SF0.25 TSV files and schema digest. Create new
private disposable PG18.3 and current-Goopg clusters on unused paths/ports;
do not reuse or mutate R107/R110 data directories. Load the same four Q96
tables from those files, prove pre-ANALYZE counts and primary-key-ordered
type-aware full relation witnesses equal under R109's canonicalizer, and
retain raw and canonical ordered-COPY hashes per relation. Prove and gate
schema and index equivalence before ANALYZE. Record all binaries,
SQL/input/canonicalizer hashes, connection rendering settings, load commands,
schema/index witnesses, and service shutdown.

Then run native ANALYZE separately in each engine, recording—not equating—all
relevant table and predicate/join-column statistics plus planner GUCs. Capture
the immutable R101 forced forms twice in each engine with values and structured
plans where supported. Assert value 266, form repeatability, forced first-two
leaves, and final parameterized time probe. Stop and report if any full
relation witness, schema/index witness, required statistic capture, or semantic
control fails.

## Boundaries

No production source, cost/selectivity, path/election, executor, benchmark
SQL, `postgres/`, reference/data mutation, GUC/default, hint, or disabled-join
method change is authorized. This establishes evidence only, never a cost
change. Before measurement: agent review, correction if needed, `git commit
-n`, and push. Afterwards commit/push an English report and TODO update
separately from unrelated work.
