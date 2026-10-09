CREATE TEMP TABLE xa (k int, v int, s int);
CREATE TEMP TABLE xb (k int, w int);
INSERT INTO xa SELECT i, i%97, i%7 FROM generate_series(1,2000) i;
INSERT INTO xb SELECT i, i FROM generate_series(1,2000) i;
ANALYZE xa; ANALYZE xb;
SET max_parallel_workers_per_gather=0;
EXPLAIN (COSTS OFF) WITH r AS (SELECT k, s, sum(v) t FROM xa GROUP BY k, s)
 SELECT count(*) FROM r r1, xb WHERE r1.k = xb.k AND r1.t > (SELECT avg(t) FROM r r2 WHERE r2.s = r1.s);
WITH r AS (SELECT k, s, sum(v) t FROM xa GROUP BY k, s)
 SELECT count(*) FROM r r1, xb WHERE r1.k = xb.k AND r1.t > (SELECT avg(t) FROM r r2 WHERE r2.s = r1.s);
