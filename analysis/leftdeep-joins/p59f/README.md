# M0127-P5.9-f — evidence

Captured 2026-08-05. Two independent defects on the seam where `unnestSubquery`
splices a decorrelated aggregate onto the finished join tree. Write-up:
`docs/design/leftdeep-joins/09-verification-and-acceptance.md` §5.21.

## Files

| file | what |
|---|---|
| `arm-on.txt` | TPC-H SF1 Q17, `GOOPG_PGSHAPED_DP=1`, post-fix engine `69b9f548e04161c8` |
| `arm-off.txt` | the flag-OFF control, **same binary**, same server age protocol |
| `ds05.txt` | full TPC-DS SF0.5 regression sweep with both fixes in |

## The bar

```
$ tmp/tpch-acceptance-runner -diff arm-off.txt arm-on.txt
  Q17  MATCH        rows=1
SUMMARY: 1 MATCH
VERDICT: PASS — every label matched on values, not merely on row count
```

`OK elapsed=33.46s` (ON) vs `OK elapsed=32.98s` (OFF), both `rows=1`,
`ordered=acb1af46ffdeef81`. There is no Q17 timing regression — the last thread
of the withdrawn "157×" figure.

## The reproducer (~1 s, not the 28 s arm)

A throwaway cluster on 127.0.0.1:5533 with TPC-H-shaped `lineitem` (16 cols,
3000 rows) and `part` (9 cols, 200 rows), loaded so that exactly five rows
satisfy Q17:

```sql
insert into part select g, 'name'||g, 'mfgr',
  case when g%7=0 then 'Brand#23' else 'Brand#11' end, 'type', g%50,
  case when g%5=0 then 'MED BOX' else 'JUMBO BAG' end, 100.0+g, 'c'
  from generate_series(1,200) g;
insert into lineitem select g/3+1, (g%200)+1, g%10+1, g%7, (g%17)+1.0, 100.0+g,
  0.05, 0.02, 'N','O', date '1995-01-01', date '1995-01-02', date '1995-01-03',
  'DELIVER IN PERSON','AIR','cmt' from generate_series(1,3000) g;
```

`l_quantity = (g%17)+1` rather than `(g%50)+1` on purpose: with modulus 50 the
quantity is a function of `l_partkey` (which is `g%200`), every qualifying part
lands on the same quantity, and the query matches nothing regardless of whether
the engine is correct — a fixture that cannot fail is not a fixture.

Three engine states, same data, `analyze part; analyze lineitem;` in-session
(stats are per-connection):

| engine | flag ON | flag OFF |
|---|---|---|
| pre-fix | `ERROR: column ref l_quantity/29 out of VirtualSlot range 27` | 5 rows, `avg_yearly = 945.7142857142857143` |
| defect-1 fix only | **0 rows**, `avg_yearly` NULL | 5 rows, 945.714… |
| both fixes | 5 rows, 945.714… | 5 rows, 945.714… |

The middle row is why the loop did not stop at the reported symptom.

## Incidental discovery — NOT a P5.9-f defect

Found while reducing the fixture; ledgered separately, unrelated to the join
search. `char(N)` typmods are not restored per column on catalog reload:

```
$ goopg init -D d --no-sync && goopg start -D d --listen 127.0.0.1:5534
psql> create table ct (a char(1), b char(25));
psql> insert into ct values ('N','DELIVER IN PERSON');     -- INSERT 0 1
$ goopg stop -D d && goopg start -D d --listen 127.0.0.1:5534
psql> insert into ct values ('N','DELIVER IN PERSON');     -- ERROR: value too
                                                           -- long for type
                                                           -- character(1)
psql> \d ct    -- still reports character(1) / character(25) correctly
```

So the catalog *reports* the right typmods after reload while the length check
applies the wrong one — the failure names `character(1)` for a value bound for
the `character(25)` column. PG restores `atttypmod` per attribute from
`pg_attribute`. Resume point is in the ledger row.
