drop table if exists rp_o, rp_a, rp_b;
create table rp_o(k int, g int); insert into rp_o select g, g % 5 from generate_series(1,20) g;
create table rp_a(k int, g int, v int); insert into rp_a select g, g % 5, g from generate_series(1,2000) g;
create table rp_b(g int, w int); insert into rp_b select g % 5, g from generate_series(1,500) g;
create index rp_a_g on rp_a(g);
analyze rp_o; analyze rp_a; analyze rp_b;
-- (1) hash join inside the body, correlated restriction on one side
select o.k, (select count(*) from rp_a a join rp_b b on a.g = b.g where a.g = o.g and a.k < 100) c from rp_o o order by o.k;
-- (2) correlated leaf under an aggregate + sort inside the body
select o.k, (select sum(v) from (select v from rp_a a where a.g = o.g order by v limit 3) s) c from rp_o o order by o.k;
-- (3) correlated leaf under a nested loop with materialize
select o.k, (select count(*) from rp_a a, rp_b b where a.g = o.g and b.g = o.g and a.k < 50 and b.w < 50) c from rp_o o order by o.k;
-- (4) correlated CTE (M0146-0050 shape)
select o.k, (select sum(v) from (with c as materialized (select v from rp_a a where a.g = o.g and a.k < 30) select v from c) z) c from rp_o o order by o.k;
-- (5) correlated leaf on the build side of a hash join with an uncorrelated probe side
select o.k, (select count(*) from rp_b b join (select g from rp_a a where a.k = o.k) a2 on a2.g = b.g) c from rp_o o order by o.k;
-- (6) LATERAL with hash aggregate over correlated leaf
select o.k, l.c from rp_o o, lateral (select g, count(*) c from rp_a a where a.g = o.g and a.k < 40 group by g) l order by o.k;
-- (7) EXISTS with correlated leaf joined to a grouped subquery
select count(*) from rp_o o where exists (select 1 from (select g, max(w) mw from rp_b group by g) b join rp_a a on a.g = b.g where a.k = o.k and b.mw > 490);
