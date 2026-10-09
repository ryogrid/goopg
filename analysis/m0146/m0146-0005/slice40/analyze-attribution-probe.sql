SET enable_hashjoin = off; SET enable_mergejoin = off; SET max_parallel_workers_per_gather = 0; SET join_collapse_limit = 1; SET enable_bitmapscan = off;
EXPLAIN (ANALYZE, COSTS OFF, TIMING OFF, SUMMARY OFF)
SELECT count(*) FROM (pa_o JOIN pa_z ON pa_z.zk = pa_o.k) JOIN pa_i ON pa_i.k = pa_o.k AND pa_i.w + pa_o.k % 3 <> pa_o.v AND pa_i.w + pa_z.zv > 1;
SELECT count(*) FILTER (WHERE NOT (i.w + o.k % 3 <> o.v)) AS probe_rej,
       count(*) FILTER (WHERE (i.w + o.k % 3 <> o.v) AND NOT (i.w + z.zv > 1)) AS join_rej,
       count(*) FILTER (WHERE (i.w + o.k % 3 <> o.v) AND (i.w + z.zv > 1)) AS out_rows
FROM pa_o o JOIN pa_z z ON z.zk = o.k JOIN pa_i i ON i.k = o.k;
