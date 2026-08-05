# M0127-P5.9-g — evidence

Captured 2026-08-05. TPC-H Q2 returned **0 rows under `GOOPG_PGSHAPED_DP=1`
against 455** — run 2's last clause-1 failure that was actually the flag's.
Write-up: `docs/design/leftdeep-joins/09-verification-and-acceptance.md` §5.22.

## Files

| file | what |
|---|---|
| `arm-off.txt` | TPC-H SF1 Q2, `GOOPG_PGSHAPED_DP=0`, post-fix engine `c8fe0d352d75b67e` |
| `arm-on.txt` | the flag-ON arm, **same binary**, same server-age protocol |

## The bar

```
$ tmp/tpch-acceptance-runner -diff analysis/leftdeep-joins/p59g/arm-off.txt \
                                   analysis/leftdeep-joins/p59g/arm-on.txt
  Q2  MATCH        rows=455
SUMMARY: 1 MATCH
VERDICT: PASS — every label matched on values, not merely on row count
```

Both arms `ordered=1c0f630719e8c7bf`, `colsig=98559dc0378d935e`. Elapsed
2.43 s (OFF) vs 3.36 s (ON) — a clause-3 matter for P5.9-h, not a correctness
one.

## The reproducer (~1 s, not the SF1 arm)

A throwaway cluster on 127.0.0.1:5533 with all five of Q2's relations at their
real TPC-H column layouts, 2353 partsupp rows, 18 qualifying parts — **and the
five primary-key indexes**:

```sql
insert into region values (0,'AFRICA','c0'),(1,'AMERICA','c1'),(2,'ASIA','c2'),
                          (3,'EUROPE','c3'),(4,'MIDDLE EAST','c4');
insert into nation   select g, 'NATION'||g, g%5, 'nc'||g from generate_series(0,24) g;
insert into supplier select g, 'Supplier#'||g, 'addr'||g, g%25, 'phone'||g,
                            1000.0+g, 'scmt'||g from generate_series(1,200) g;
insert into part     select g, 'name'||g, 'Manufacturer#'||(g%5), 'Brand#'||(g%5),
                            case when g%3=0 then 'SMALL PLATED BRASS'
                                 else 'LARGE ANODIZED STEEL' end,
                            case when g%4=0 then 15 else (g%50) end,
                            'MED BOX', 100.0+g, 'pc'||g from generate_series(1,200) g;
insert into partsupp select p, s, 100+p, (((p*7+s*13)%97)+1)::numeric, 'psc'
                     from generate_series(1,200) p, generate_series(1,200) s
                     where (p+s)%17 = 0;

create unique index part_pk     on part(p_partkey);
create unique index supplier_pk on supplier(s_suppkey);
create unique index partsupp_pk on partsupp(ps_partkey, ps_suppkey);
create unique index nation_pk   on nation(n_nationkey);
create unique index region_pk   on region(r_regionkey);
```

**The indexes are load-bearing.** Without them the correlation
`ps_partkey = p_partkey` stays in a Filter, whose coordinate space already IS
the decorrelated aggregate's input, and both arms return 18 rows — a fixture
that cannot fail. It is only once `partsupp_pk` exists and the inner planner
folds the correlation into an `*IndexScan` probe that `harvestIndexKeyParams`
records the leaf-relative `ps_partkey/0` the bug needs. The first reduction of
this fixture omitted them and nearly retired the hypothesis. (Same trap as the
`l_quantity` modulus in `../p59f/README.md`.)

Three engine states, same data, `analyze` each table by name in-session
(stats are per-connection):

| engine | flag ON | flag OFF |
|---|---|---|
| no indexes, pre-fix | 18 rows | 18 rows |
| PK indexes, pre-fix | **0 rows** | 18 rows |
| PK indexes, post-fix | 18 rows | 18 rows |

Both post-fix arms are byte-identical (`md5 7ba8f174…`), and PG 18.3 on the
same fixture returns the same 18 tuples in the same order — differing only in
`char(N)` blank padding and numeric scale, two pre-existing goopg formatting
gaps unrelated to this seam.

## The defect in one line

The decorrelated `HashAggregate` grouped on `ps_partkey/0` while its own
argument read `min(ps_supplycost/17)` — **key and argument in different
coordinate scopes inside a single plan node.** Offset 0 of the rotated
19-column subquery body is `r_regionkey`, so every European row collapsed into
one group keyed `3`, and `part.p_partkey = 3` matched nothing.

## Incidental discovery — NOT a P5.9-g defect

`DROP INDEX` fails on an index that survived a restart, while `pg_indexes`
still lists it and the planner still uses it:

```
psql> create unique index part_pk on part(p_partkey);
$ goopg stop -D d && goopg start -D d --listen 127.0.0.1:5533
psql> drop index part_pk;                        -- ERROR: index "part_pk" does not exist
psql> select indexname from pg_indexes where schemaname='public';   -- still lists part_pk
```

Two catalog readers disagreeing about whether the object exists. This cost a
bisect round whose every iteration silently ran with all five indexes present.
Ledgered separately; resume point is in the ledger row.
