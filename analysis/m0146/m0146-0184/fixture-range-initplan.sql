DROP TABLE IF EXISTS rq;
CREATE TABLE rq (x int, pad text);
INSERT INTO rq SELECT g, 'p' FROM generate_series(1, 20000) g;
ANALYZE rq;
SET max_parallel_workers_per_gather = 0;
EXPLAIN SELECT x FROM rq WHERE x >= (SELECT 100) AND x <= (SELECT 200);
EXPLAIN SELECT x FROM rq WHERE x > (SELECT 100) + 1 AND x < (SELECT 200);
EXPLAIN SELECT x FROM rq WHERE x >= (SELECT 100);
