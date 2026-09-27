# M0146-0015f — bpchar blank-insensitive equality + hash parity (evidence)

Design: `docs/design/0100-0149/m0146-0015f-bpchar-hash-semantics-parity.md`.
Fix-plan task: "bpchar HASH semantics parity — verify/fix blank-insensitive
hashing wherever bpchar keys are grouped, joined, or de-duplicated" (filed
by `7e5064cd0` after the padding-correct SF0.25 reload).

## Repro (pre-fix)

```
postgres=# select 'Javier  '::char(20) = 'Javier';   -- goopg: f, PG: t
postgres=# select count(*) from customer where c_first_name='Javier';
-- goopg: 0, PG: 31
```

Post-reload sweep before the fix: `PASS=70 MISMATCH=20 CKMISMATCH=6`
(sweep-20260927-114444.txt) — the dominant failure mode was
`goopg=0 oracle=100` on char(n) predicates.

## Probe matrix (goopg scratch :5533 vs PG 18.3 :65438, identical)

| label | PG | goopg (post-fix) |
|---|---|---|
| `'x  '::bpchar = 'x'::bpchar` | t | t |
| `'x  '::bpchar = 'x'` (bare lit) | t | t |
| `'x  '::text = 'x'::bpchar` | f | f |
| `bpchar_col IN ('x','x  ')` | t | t |
| correlated scalar `(i2.m = i1.m)` char=char | per-row | identical |
| correlated scalar char(20)=char(5) | per-row | identical |
| `IN (subquery)` semi join | 2 | 2 |
| hash join char(20)=char(5) | 4 | 4 |
| multi-col `(m, n) IN (subquery)` | 1 | 1 |
| GROUP BY over char(20)∪char(5) `UNION ALL` | 2 groups | 2 |
| `UNION` dedup char(20) vs char(5) | 1 | 1 |
| window `partition by char(20) col` | 1 part | 1 |
| `'x  '::bpchar IS DISTINCT FROM 'x'::bpchar` | f | f |
| `'x  '::text IS DISTINCT FROM 'x'::bpchar` | t | t |

## Gates (staged tree = HEAD + bpchar code)

- `RALPH_PRECOMMIT_SCOPE=units` — PASS (executor 18.7 s, initdb 167 s).
- `scripts/tpch-spotcheck.sh` — Q12=2, Q13=33, PASS (stamped).
- `ACCEPT_BASELINE=bench/tpch/baseline-digests.txt
  scripts/tpch-acceptance-arm.sh on` — 24/24 value MATCH, PASS
  (stamped; arm `tmp/arm-on-m0146-0015f.txt`).
- `scripts/tpcds-sf025-regression.sh sweep` — **PASS=96 MISMATCH=0
  CKMISMATCH=0 ERROR=0 TIMEOUT=0 SKIP=3** (sweep-20260927-130805.txt,
  stamped); plan-shape channel `same=99 changed=0`; status-delta
  `+Q41` vs the pre-ExecParamRef sweep.
- Live server-side probes — table above.

## Focused tests

- `internal/executor/bpchar_hash_parity_test.go` — hash join
  cross-width, GROUP BY/DISTINCT/SetOp/recursive-CTE dedup, hashed +
  linear + multi-column subquery IN, corr-subq hash map, window
  partition keys, text coercion arm.
- `TestHashJoinBpcharTrailingSpaceAgreement` (hash_join_bpchar_test.go)
  — re-pinned to PG's post-fix truth: scalar `=` t, join matches.
- `bpchar_padded_storage_test.go` — padded storage preserved on disk;
  only comparison/key images trim.

## Ledgered residual

`op ANY(array-datum)` (a `bpchar[]` column or multi-element
`ARRAY[...]` literal) carries no declared element type, so the element
side compares byte-exact while a declared-bpchar operand trims —
`x = ANY(bpchar_col_array)` can miss padded-equal elements. Scalar
`IN` lists and subquery `IN` carry item types and are covered. Row in
`.ralph/deferral_ledger.md` under M0146-0015f.
