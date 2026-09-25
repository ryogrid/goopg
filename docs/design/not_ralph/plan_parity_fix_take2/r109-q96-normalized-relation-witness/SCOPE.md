# R109 SCOPE — type-normalized common-data witness for Q96

R107 loaded identical TSVs with equal row counts, and every Q96-referenced
projection matched, but a raw full `COPY TO STDOUT` witness diverged: PG emits
`char(n)` padding and numeric display scale differently from Goopg. That is
insufficient evidence of a logical data mismatch, but raw output cannot serve
as the required common-data witness.

## Authorized measurement

Against R107's disposable clusters only, derive a deterministic, ordered,
complete per-table witness from `tpcds.sql` types and the `COPY` output. Its
client-side canonicalizer must first parse PostgreSQL text-COPY row framing and
escaping (`\N`, delimiter/newline/backslash escapes), then normalize fields,
and hash an explicitly specified canonical re-encoding rather than manipulate
TSV lines. It may remove only representation differences justified by the
declared SQL type: trailing spaces for `char(n)` and insignificant trailing
fractional zeros for finite `numeric`. It must preserve NULLs, delimiters,
embedded whitespace in non-`char` values, signs, and every non-normalized
field exactly. For each table record the complete `COPY (SELECT all declared
columns ... ORDER BY declared primary key) TO STDOUT` statement, schema/input
digests, ordered key, pre/post row counts, raw plus normalized hashes,
`client_encoding`, DateStyle, IntervalStyle, extra_float_digits, and connection
settings used for both streams.

Use PG18.3 as a normalization control: synthesize values that differ only in
allowed display representation, including `5.00` versus `5.0`, and values that
differ semantically (a value-changing digit or nonzero sign, trailing
whitespace in `varchar`, NULL versus empty, and changed COPY escapes). The
former must canonicalize together and the latter must not. Do not alter cluster
contents or run ANALYZE; R107 has already done its native ANALYZE. If any
normalized complete relation differs, stop and report it.

Matching normalized hashes prove equality only under this type-constrained
witness, not complete SQL semantic equivalence, byte/format-sensitive behavior,
or equal native statistics. Retain raw mismatch evidence. A successor may use
the witness for Q96 cost attribution only after it records why every
Q96-referenced datum and predicate is unaffected; it must not claim general
common-data semantic equivalence from normalized hashes.

## Boundaries

No production source, cost/selectivity, path/election, executor, benchmark
SQL, `postgres/`, schema, index, data, GUC, or cluster change is authorized.
The canonicalizer is a disposable measurement artifact, not a database
compatibility fix. Stop both services after collection. Before measurement:
agent review, correction if needed, `git commit -n`, and push. Afterwards
commit/push an English report and TODO update separately from unrelated work.
