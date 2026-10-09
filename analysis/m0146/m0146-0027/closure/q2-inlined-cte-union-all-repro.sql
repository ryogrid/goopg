create table ws (sd int, p numeric, pad text);
create table cs (sd int, p numeric, pad text);
create table dd (dk int primary key, wk int, pad text);
insert into ws select g % 2000, g, repeat('x',100) from generate_series(1,180000) g;
insert into cs select g % 2000, g, repeat('x',100) from generate_series(1,360000) g;
insert into dd select g, g/7, repeat('y',200) from generate_series(1,73049) g;
analyze ws; analyze cs; analyze dd;
explain with w as (select sd, p from (select sd, p from ws union all select sd, p from cs) s),
 x as (select wk, sum(p) from w, dd where dk = sd group by wk) select count(*) from x;
explain select wk, sum(p) from (select sd, p from ws union all select sd, p from cs) s, dd where dk = sd group by wk;
\echo (a) CTE body with direct union-all subquery
explain with x as (select wk, sum(p) from (select sd, p from ws union all select sd, p from cs) s, dd where dk = sd group by wk) select count(*) from x;
\echo (b) top-level with inlined CTE w
explain with w as (select sd, p from (select sd, p from ws union all select sd, p from cs) s) select wk, sum(p) from w, dd where dk = sd group by wk;
\echo (c) top-level inlined CTE w, union all directly in CTE
explain with w as (select sd, p from ws union all select sd, p from cs) select wk, sum(p) from w, dd where dk = sd group by wk;
