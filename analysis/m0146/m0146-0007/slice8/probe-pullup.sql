drop table if exists pc_t; create table pc_t(f1 int, f2 int); insert into pc_t select g, g from generate_series(1,5) g;
with x as (select 1 as y) select * from (with x as (select 2 as y) select * from x) ss;
explain (costs off) select * from pc_t where now() = now();
explain (costs off) select * from pc_t a, pc_t b where a.f1 = b.f1 and now() = now();
explain (costs off) select * from (select f1, now() n from pc_t) a, (select f1, now() n from pc_t) b where a.n = b.n;
explain (costs off) with x as not materialized (select f1, now() n from pc_t) select * from x, x x2 where x.n = x2.n;
select count(*) from (select f1, now() n from pc_t) a, (select f1, now() n from pc_t) b where a.n = b.n;
explain (costs off) select * from (select f1, 1 as k from pc_t) a, (select f1, 1 as k from pc_t) b where a.k = b.k;
explain (costs off) select * from (select f1, 1 as k from pc_t) a, pc_t b where a.k = b.f1;
