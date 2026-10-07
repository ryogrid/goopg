\i tmp/m93-setup.sql
-- (a) single-table member with WHERE, subset consumption
explain (costs off) select m, sum(price) from (select v as price, k as dk, pad from s1 where k > 5 union all select v, k, pad from s2) u, d where d.k = u.dk group by m;
-- (b) same, full in-order consumption
explain (costs off) select price, dk, pad from (select v as price, k as dk, pad from s1 where k > 5 union all select v, k, pad from s2) u;
-- (c) join member, full consumption in order
explain (costs off) select price, dk from (select s1.v as price, s1.k as dk from s1, d where d.k = s1.k union all select s2.v, s2.k from s2) u;
-- (d) join member, out-of-order consumption
explain (costs off) select dk, price from (select s1.v as price, s1.k as dk from s1, d where d.k = s1.k union all select s2.v, s2.k from s2) u;
-- (e) join member under a join with subset
explain (costs off) select count(*) from (select s1.v as price, s1.k as dk from s1, d where d.k = s1.k union all select s2.v, s2.k from s2) u, d d2 where d2.k = u.dk;
-- (f) statement-level union all with a join arm
explain (costs off) select s1.v, s1.k from s1, d where d.k = s1.k union all select s2.v, s2.k from s2;
explain (costs off) select m, sum(price) from (select v as price, k as dk from s1 union all select v as price, k as dk from s2) u, d where d.k = u.dk group by m;
explain (costs off) select m, sum(price) from (select v as price, k as dk, 1 as src from s1 union all select v as price, k as dk, 2 as src from s2) u, d where d.k = u.dk and src > 0 group by m;
explain (costs off) select m, sum(price) from (select k as dk, v as price from s1 union all select k, v from s2) u, d where d.k = u.dk group by m;
explain (costs off) select m, sum(price) from (select s1.v as price, s1.k as dk, s1.pad as p from s1, d where d.k = s1.k and d.m = 3 union all select s2.v, s2.k, s2.pad from s2, d where d.k = s2.k and d.m = 3) u, d d2 where d2.k = u.dk group by m;
