DROP TABLE IF EXISTS iu_big, iu_small;
CREATE TABLE iu_small (a int, b int, v int, PRIMARY KEY (a, b));
CREATE TABLE iu_big (a int, b int, w int);
INSERT INTO iu_small SELECT g % 100, g / 100, g FROM generate_series(1, 10000) g;
INSERT INTO iu_big SELECT g % 100, g / 100, g FROM generate_series(1, 100000) g;
ANALYZE iu_small; ANALYZE iu_big;
SET enable_mergejoin = off; SET enable_nestloop = off; SET max_parallel_workers_per_gather = 0;
EXPLAIN SELECT s.v, b.w FROM iu_small s LEFT JOIN iu_big b ON s.a = b.a AND s.b = b.b;
