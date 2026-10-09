set enable_hashjoin=off; set enable_mergejoin=off; set max_parallel_workers_per_gather=0; set enable_bitmapscan=off; set enable_memoize=off;
explain (analyze, timing off, costs off) select sum(i.v) from o join i on i.k = o.id;
explain (analyze, timing off, costs off) select sum(i.v) from o join i on i.k = o.id;
explain (analyze, timing off, costs off) select sum(i.v) from o join i on i.k = o.id;
