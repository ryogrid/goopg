drop table if exists m7t, m7u;
create table m7t(a int, b int); insert into m7t select g, g%10 from generate_series(1,1000) g;
create table m7u(a int, b int); insert into m7u select g, g%7 from generate_series(1,500) g;
analyze m7t; analyze m7u;
-- simple body, two references
explain (costs off) with x as not materialized (select a, b from m7t where b < 5) select * from x, x x2 where x.a = x2.a and x2.b = 1;
with x as not materialized (select a, b from m7t where b < 5) select count(*), sum(x.a) from x, x x2 where x.a = x2.a and x2.b = 1;
-- aggregate body, two references
explain (costs off) with x as not materialized (select b, count(*) c from m7t group by b) select * from x, x x2 where x.b = x2.b + 1 and x2.b = 3;
with x as not materialized (select b, count(*) c from m7t group by b) select * from x, x x2 where x.b = x2.b + 1 and x2.b = 3;
-- default (no keyword) two refs stays a CTE
explain (costs off) with x as (select a, b from m7t where b < 5) select * from x, x x2 where x.a = x2.a and x2.b = 1;
-- not materialized, one ref in a sublink
explain (costs off) with x as not materialized (select a, b from m7t where b < 5) select * from x where x.a in (select a from x x2 where x2.b = 2);
with x as not materialized (select a, b from m7t where b < 5) select count(*) from x where x.a in (select a from x x2 where x2.b = 2);
-- not materialized, refs inside a JOIN chain
explain (costs off) with x as not materialized (select a, b from m7t where b < 5) select * from x join m7u on x.a = m7u.a join x x2 on x2.a = m7u.a where x2.b = 1;
with x as not materialized (select a, b from m7t where b < 5) select count(*) from x join m7u on x.a = m7u.a join x x2 on x2.a = m7u.a where x2.b = 1;
-- volatile prevents
explain (costs off) with x as not materialized (select a, random() r from m7t) select * from x, x x2 where x.a = x2.a;
-- body referencing another CTE, both not materialized multi-ref
explain (costs off) with y as not materialized (select a, b from m7u), x as not materialized (select y.a, y.b from y, m7t where y.a = m7t.a) select * from x, x x2, y where x.a = x2.a and y.a = x.a and x2.b = 3;
with y as not materialized (select a, b from m7u), x as not materialized (select y.a, y.b from y, m7t where y.a = m7t.a) select count(*) from x, x x2, y where x.a = x2.a and y.a = x.a and x2.b = 3;
-- column alias list
explain (costs off) with x(p, q) as not materialized (select a, b from m7t) select * from x, x x2 where x.p = x2.p and x2.q = 1;
-- self name
drop table if exists m7s; create table m7s(a int); insert into m7s values (1),(2);
with m7s as not materialized (select a+10 as a from m7s) select * from m7s, m7s s2 where m7s.a = s2.a;
-- outer self-ref (recursive)
explain (costs off) with recursive x(a) as ((values ('a'), ('b')) union all (with z as not materialized (select * from x) select z.a || z1.a as a from z cross join z as z1 where length(z.a || z1.a) < 5)) select * from x;
with recursive x(a) as ((values ('a'), ('b')) union all (with z as not materialized (select * from x) select z.a || z1.a as a from z cross join z as z1 where length(z.a || z1.a) < 5)) select * from x;
