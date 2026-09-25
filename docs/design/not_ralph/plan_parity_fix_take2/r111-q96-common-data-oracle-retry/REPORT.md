# R111 result: rebuilt Q96 input oracle is valid after varchar repair

R111 rebuilt both engines from the immutable R107 SF0.25 inputs after R110's
varchar assignment/COPY repair. The pre-ANALYZE schema, indexes, row counts,
and type-aware complete relation witnesses now agree. This clears the
common-data prerequisite for a separately scoped R108 PG Datum/hash-tuple
planning-geometry experiment. It does not itself change a cost or elect a
plan.

## Reproduction and input gates

Source commit was `33177124c`. Current Goopg was built with `go build -o
/tmp/r111-goopg ./cmd/goopg`; its SHA-256 was
`6fb87884736064a1d140738bee98dcac655b7cf9f735132ec131df1a7fa4f8d5`.
A private PG18.3 cluster ran at `/tmp/r111-pg18-q96`, port 65445, and a new
private Goopg cluster ran at `/tmp/r111-goopg-q96`, port 5566. Neither R107
nor R110 data directory was reused. Both were stopped after collection.

Each cluster loaded the same schema
`third-party/tpcds-postgres/DSGen-software-code-3.2.0rc1/tools/tpcds.sql`
(SHA-256 `508f77e089457098bd22d4e6e80ec1cf5f624e16e2eba482f4f5c4451269f3ae`)
and the four TSVs through `COPY ... WITH (FORMAT text, DELIMITER E'\\t',
NULL E'\\\\N')`:

| table | TSV SHA-256 | rows in PG and Goopg |
| --- | --- | ---: |
| store_sales | `4fad54b71d7bb5da2f84475341efa9e11be6a728de82dc3c2d27058767fc7585` | 719876 |
| household_demographics | `8a0cf9f91fcf1667c9909d2acf690366d643f4f6ca5c1fe2dcc2669818569f24` | 7200 |
| store | `51d73e20723dd95073a7152e207f28e582d216bc1ee3496429604d64171dbba9` | 12 |
| time_dim | `466e14f7d7630a6c0e9e297cbd647c5747091cdabf31169ada947558d57ce107` | 86400 |

Before `ANALYZE`, the ordered catalog witnesses were byte-identical: columns
`(relname, attnum, attname, atttypid, atttypmod, attnotnull)` SHA-256
`c0634080fab79296f32a00b213e7de9f6dc6236ac1bfc0e1c4045ec58daaf132`, and
indexes `(table, indisprimary, indisunique, indkey)` SHA-256
`d17ccc138df1dcbb2f2cf9f7f81bbd927ca6c2346aeaf2d51735315b131f93ac`.

The R109 canonicalizer (`/tmp/r109_copy_canonicalize.py`, SHA-256
`1e8a8a925844b61ed57a0094751a404623ce048a76d15c5703a8fc310b6959d6`)
length-frames decoded text COPY values, trims only `char(n)` padding, and
normalizes only finite numeric trailing fractional zeroes. It preserves every
varchar byte. Primary-key-ordered complete relation witnesses were:

| table | PG raw SHA-256 | Goopg raw SHA-256 | shared canonical SHA-256 |
| --- | --- | --- | --- |
| store_sales | `d33505b2864e86ffd513267931caaa3a6a2a6723d81283e22f89ff87a34ba3df` | same | `27b58afeba052316b70171975efaa5004fe7f3970575b084a5e6f9d23d6271b0` |
| household_demographics | `3dda6ba60365c7f0b074a6af5d7887eca19080ba5f0835eccb90d97fa34407ca` | same | `429944eff57e3d75231f127181df05d42e57f974d969aedd3abe3abc020c29b5` |
| store | `cb15873523c81bd446d244e4cd49ac981f195a403f9a5c2ed790652b7363a6e0` | `1e9bcb45cc785950df369793c32397fd2e2f99bb7fdb90852321d90bd91a9d86` | `040c1a49656da7c6052629f42d0bf0858220da6df38f8a4a782bbb58d4e51696` |
| time_dim | `b995c1e120832f1e37814dcc628553333b64df009cdfccd8a0b31f8416aa2f53` | same | `9cb0088158a0601f400b4654234b585818a2fa37e80bbd55973c7fc016b0f011` |

The remaining raw `store` difference is the intentionally normalized
`char(n)`/numeric text representation, not varchar loss. Both clients
reported PG18.3, UTF8, `DateStyle=ISO, MDY`, `IntervalStyle=postgres`, and
`extra_float_digits=1`.

## Native statistics and forced-form controls

Both engines ran native `ANALYZE` on the four loaded tables. The selected Q96
predicate/join-column `pg_stats` captures have SHA-256
`8584e8080bce9283e684c5e2c2a3c54c9f94c6a926d27c59017e45bbafa2e765` (PG)
and `9ad1e4b8a05e2fcc4b3eae27db598da98f9ed5d1bfac5db73309abb25dafb3ea`
(Goopg); they are recorded, not asserted equal. Their planner-GUC capture
hashes are respectively
`8485801cc7f87d31579aaae6cb28133850dc5c772f458ed285dc6693965a2f3e` and
`57b3cd43508a04f3ccf8c295532e7e8e0484350e1e0f93ec88c31947d7b7ee8c`.

The immutable R101 forms retained their recorded SHA-256 values:
`hdem-first` `d83c3d58d7d4b543d68908da78de79bad900eb230e0dbe830faf27eec6293784`
and `store-first` `cda057e138b1fd9f9731b7249ea893a257f5c1c9561cffea2b5b8182fc27d5d6`.
PG sessions set `work_mem=64MB`, `max_parallel_workers_per_gather=4`,
`parallel_leader_participation=on`, and both collapse limits to 1. Goopg used
both collapse limits at 1 and server controls `GOOPG_GATHER_PATHS=top`,
`GOOPG_PARTIAL_AGG_PATHS=on`, `GOGC=off`, and `GOMEMLIMIT=12GiB`.

Each engine's text and JSON EXPLAIN were byte-identical across two runs, every
form returned 266, and the forced leaf sequence plus final parameterized
`time_dim_pkey` probe were present. The root total costs were:

| form | PG18.3 | Goopg |
| --- | ---: | ---: |
| hdem-first | 17669.88 | 27661.94 |
| store-first | 17845.79 | 27654.26 |

Thus this clean common-input capture preserves the relevant forced-order
margin disagreement: PG prices hdem-first lower by 175.91, while Goopg prices
store-first lower by 7.68. This is evidence for R108's explicitly opt-in
geometry measurement only; it is not yet evidence that any particular
Datum-size change is correct.

## Ruling

**PASS: the R111 common-data, schema/index, statistics-capture, and semantic
control gates passed.** R108 may now be scoped and reviewed. Its experiment
must retain this input oracle and separately demonstrate plan/value deltas
before any production cost-model decision.
