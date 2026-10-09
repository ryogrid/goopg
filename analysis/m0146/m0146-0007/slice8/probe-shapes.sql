explain (costs off) select * from pc_t where 1 = 2;
explain (costs off) select * from pc_t where current_user = 'postgres';
explain (costs off) select * from pc_t where now() > '2000-01-01'::timestamptz and f1 > 2;
explain (costs off) select * from pc_t where random() > 2;
explain (costs off) select * from pc_t where length('abc') = 3;
explain (costs off) select * from pc_t where now() = now() order by f1 limit 2;
explain (costs off) select count(*) from pc_t where now() = now();
explain (costs off) select * from pc_t a left join pc_t b on a.f1 = b.f1 and now() = now();
explain (costs off) select f1 from pc_t where now() = now() union all select f2 from pc_t;
