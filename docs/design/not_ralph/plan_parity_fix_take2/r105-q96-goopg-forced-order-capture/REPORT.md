# R105 result: repeatable Goopg forced-Q96 cost capture

R104 made R101's two unchanged forced forms executable on Goopg. This report
captures their post-R104 costs reproducibly; it makes no planner change and
does not treat a forced SQL order as natural-election evidence.

## Reproduction

Source commit: `0d7eed64b81abfa090a88db75b5d49a5111c530c`. The private binary
was built as `/tmp/r105-goopg` with `go build -o /tmp/r105-goopg ./cmd/goopg`;
its SHA-256 was
`eb906663191aee0b5a0ab1a13e381b58c653633390a3bf31ffe9fbcef446a3aa`.
It served the isolated existing SF0.25 clone `/tmp/r97goopg/ds025` on
`127.0.0.1:5563` in transient user service `goopg-r105.service`, and was
stopped after collection. No source dataset or SQL file was modified and no
ANALYZE ran.

Controls were `GOOPG_GATHER_PATHS=top`, `GOOPG_PARTIAL_AGG_PATHS=on`,
`GOGC=off`, and `GOMEMLIMIT=12GiB` (with service `MemoryHigh=20G`,
`MemoryMax=24G`, and swap disabled). The client was
`postgres/local_install/bin/psql -X -A -t`; each plan came from prepending
`EXPLAIN ` to the immutable source file.

| form | SQL path | SQL SHA-256 | EXPLAIN run 1 = run 2 SHA-256 | value |
| --- | --- | --- | --- | ---: |
| hdem-first | `/tmp/r101-hdem-first.sql` | `d83c3d58d7d4b543d68908da78de79bad900eb230e0dbe830faf27eec6293784` | `30e10cb916a12267373b5f3976b2a7ee16483b374ab8f1e354aa83a96ed20c13` | 266 |
| store-first | `/tmp/r101-store-first.sql` | `cda057e138b1fd9f9731b7249ea893a257f5c1c9561cffea2b5b8182fc27d5d6` | `2b35b93c26f960395d68128e8b66ec92ec0f385551c20663f99cb05de9` | 266 |

Each repeated pair was byte-identical because the hashes were calculated over
the complete `psql -A -t` EXPLAIN output. The durable hashes above, plus the
complete operator spines below, are the captured plan evidence.

## Captured operator spines and costs

hdem-first root cost is **27639.23**. Its forced first hash join is
`store_sales × household_demographics`: child costs/rows are
`7198.76/719876` and `72.00/7200`, output `14148.45/687769`. The second hash
join adds `store`: child cost/rows `0.12/12`, output `27598.12/657186`. A
`Nested Loop` then probes `time_dim_pkey` with
`t_time_sk = store_sales.ss_sold_time_sk` and filters `t_hour = 8` and
`t_minute >= 30`; its cost/rows are `27631.00/3285`.

store-first root cost is **27641.15**. Its forced first hash join is
`store_sales × store`: child costs/rows are `7198.76/719876` and `0.12/12`,
output `14077.53/687865`. The second hash join adds
`household_demographics`: child cost/rows `72.00/7200`, output
`27600.04/657186`. It has the same final parameterized `time_dim_pkey` probe;
the `Nested Loop` is `27632.92/3285`.

Thus Goopg prices hdem-first lower by **1.92** root-cost units
(`27641.15 - 27639.23`). Native PG18.3's R101 forced measurement instead had
a 176.16-unit hdem-first advantage. This exposes a forced-shape cost-margin
difference only. It does not establish a natural join-order mismatch, nor
authorize a cost, selectivity, or election change.
