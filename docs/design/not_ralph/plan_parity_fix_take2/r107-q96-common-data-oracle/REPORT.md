# R107 result: Q96 projection matches, but full-relation oracle is invalid

R107 created fresh disposable clusters from the same SF0.25 TSV input, but it
did not establish the required full relation equivalence. The stop rule
therefore applies: no forced-plan cost margin is compared and no planner
change follows from this run.

## Reproduction

Source commit was `6e79ad1c4`. Native PG was PostgreSQL 18.3 at private
`/tmp/r107-pg18-q96`, port 65443; Goopg was built as `/tmp/r107-goopg` and ran
at private `/tmp/r107-goopg-q96`, port 5564. The Goopg binary SHA-256 was
`b900e443308c8d9b70c5572f62468d0c9f0bc4475235bcec9d0c9e12f88876c8`.
Both services were stopped after collection. Each loaded the same schema file
`third-party/tpcds-postgres/DSGen-software-code-3.2.0rc1/tools/tpcds.sql`
(SHA-256 `508f77e089457098bd22d4e6e80ec1cf5f624e16e2eba482f4f5c4451269f3ae`)
and then `COPY`ed these Q96 tables before native `ANALYZE`:

| table | TSV SHA-256 | input and post-load rows in both engines |
| --- | --- | ---: |
| store_sales | `4fad54b71d7bb5da2f84475341efa9e11be6a728de82dc3c2d27058767fc7585` | 719876 |
| household_demographics | `8a0cf9f91fcf1667c9909d2acf690366d643f4f6ca5c1fe2dcc2669818569f24` | 7200 |
| store | `51d73e20723dd95073a7152e207f28e582d216bc1ee3496429604d64171dbba9` | 12 |
| time_dim | `466e14f7d7630a6c0e9e297cbd647c5747091cdabf31169ada947558d57ce107` | 86400 |

The deterministic primary-key-ordered `COPY (SELECT * ...) TO STDOUT` witness
matched for `household_demographics` and `time_dim`, but not `store`:

| table | PG SHA-256 | Goopg SHA-256 |
| --- | --- | --- |
| store | `cb15873523c81bd446d244e4cd49ac981f195a403f9a5c2ed790652b7363a6e0` | `b4324021d6cdcd5ddb9e7d6580ac872c6fe1aeae5ad65a10635e67a0c5176b31` |

The first differing row shows PG retaining `char(n)` trailing padding (for
example `Spring  `) while Goopg emits the trimmed value (`Spring`); PG also
emits numeric `-5.00` where Goopg emits `-5`. This is a value/output-format
divergence under the full relation witness. It cannot be ignored merely
because the row counts and input files agree.

For diagnostic clarity only, the exact Q96-referenced projections did match
when primary-key ordered: `store_sales(ss_hdemo_sk, ss_store_sk,
ss_sold_time_sk)`, `household_demographics(hd_demo_sk, hd_dep_count)`,
`store(s_store_sk, s_store_name)`, and `time_dim(t_time_sk, t_hour,
t_minute)`. Their respective shared SHA-256 digests were
`6a03f65986eaf08519827564b2b630ebd3596bd1a9ee9d35d6834293425aa30e`,
`d7101453636e4493a9cb1d0eba6ecb02b22bd4144409154eb531d658cbd89f70`,
`759600314dbbdebbff3442a0a32ffbe42ad1bbb38318827d04a853d708ad9b0a`, and
`7ccb20d170c96566dacca0a71747a19965de68cdc829922f5cab8df60074653f`.
Those limited witnesses do not satisfy the scope's full-relation requirement.

## Ruling

**STOP: relation equivalence failed.** Native statistics were gathered in each
cluster but are not a comparable cost oracle after this failure; R101/R105
cost margins remain incomparable. A successor must first specify a
type-normalized complete witness that distinguishes logical SQL equality from
COPY output representation, and must prove all non-Q96 columns do not carry a
semantic divergence before using this source as a common-data planner oracle.
