create table ok2(unique1 int, stringu1 text);
insert into ok2 select g, case when g % 26 = 0 then 'A' else 'Z' end from generate_series(0,999) g;
create index ok2_u1_prtl on ok2(unique1) where stringu1 < 'B';
analyze ok2;
explain (costs off) select * from ok2 where unique1 = 51;
select * from ok2 where unique1 = 51;
select * from ok2 where unique1 = 52;
select count(*) from ok2 where stringu1 < 'B' and unique1 = 52;
