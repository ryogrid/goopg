create table t(a int, b int);
insert into t select g % 50, g from generate_series(1, 5000) g;
create table big(k int, v int);
insert into big select g % 50, g from generate_series(1, 200000) g;
analyze t; analyze big;
-- (1) grouped subquery on the hashed side, subset read
explain (costs off) select big.v from big, (select a, sum(b) s from t group by a) x where big.k = x.a;
-- (2) hashed side, full read in order
explain (costs off) select big.v, x.a, x.s from big, (select a, sum(b) s from t group by a) x where big.k = x.a;
-- (3) hashed side, full read out of order
explain (costs off) select big.v, x.s, x.a from big, (select a, sum(b) s from t group by a) x where big.k = x.a and x.s > 0;
-- (4) grouped subquery on the probe (outer) side, subset read
explain (costs off) select x.a from (select k a, sum(v) s from big group by k) x, t where t.a = x.a;
-- (5) resjunk group key on the hashed side
explain (costs off) select big.v from big, (select sum(b) s from t group by a) x where big.v = x.s;
create table tk(a int, b int, c int, d text);
insert into tk select g, g % 100, g % 7, 'x' from generate_series(1, 10000) g;
create index on tk(a);
create table t2(k int, m int);
insert into t2 select g, g % 100 from generate_series(1, 1000) g;
analyze tk; analyze t2;
set enable_mergejoin = off; set enable_nestloop = off;
explain (costs off) select count(*) from t2 left join (select * from tk order by a) y on t2.k = y.a and t2.m = y.b;
reset enable_mergejoin; set enable_hashjoin = off;
explain (costs off) select count(*) from t2 left join (select * from tk order by a) y on t2.k = y.a and t2.m = y.b;
reset enable_hashjoin; set enable_mergejoin = off; set enable_nestloop = off;
explain (costs off) select count(*) from t2 join (select a, b, sum(c) from tk group by a, b) y on t2.k = y.a;
set enable_mergejoin = off; set enable_nestloop = off;
explain (costs off, verbose) select count(*) from tk left join (select * from t2 order by k) y on tk.a = y.k;
explain (costs off, verbose) select count(*) from tk left join (select * from tk order by a) y on tk.a = y.a and tk.b = y.b and tk.c = y.c where tk.a < 100;
