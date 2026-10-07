create table t(a int, b int);
insert into t select g % 50, g from generate_series(1, 5000) g;
analyze t;
-- (1) over-keep: sublink body
explain (costs off) select * from t where b > (select max(cs) from (select a, sum(b) cs from t group by a) x);
-- (2) over-strip: grouped body under Sort
explain (costs off) select * from (select a, sum(b) s from t group by a) v order by s;
-- (3) window body under WindowAgg
explain (costs off) select a, s, rank() over (order by s) from (select a, sum(b) s from t group by a) v;
-- (4) grouped body in a join with Materialize
explain (costs off) select * from (select a, sum(b) s from t group by a) v1, (select a, max(b) m from t group by a) v2 where v1.a = v2.a and v1.s > v2.m;
-- (5) window body, outer filter
explain (costs off) select * from (select a, b, rank() over (partition by a order by b) r from t) w where r < 3;
