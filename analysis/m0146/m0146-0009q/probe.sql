drop table if exists fq_s, fq_d;
create table fq_s(sd int, st int, p numeric); insert into fq_s select g % 2000, g % 12, g from generate_series(1,400000) g;
create table fq_d(dk int, wk int); insert into fq_d select g, g / 7 from generate_series(0,1999) g;
analyze fq_s; analyze fq_d;
set parallel_setup_cost = 0; set parallel_tuple_cost = 0; set min_parallel_table_scan_size = 0;
explain with w as (select wk, st, sum(p) s from fq_s, fq_d where sd = dk group by wk, st) select * from w w1, w w2 where w1.wk = w2.wk - 52 and w1.st = w2.st;
