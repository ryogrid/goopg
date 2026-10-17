DROP TABLE IF EXISTS io_t;
CREATE TABLE io_t (k int PRIMARY KEY, pad text);
INSERT INTO io_t SELECT g, repeat('x', 50) FROM generate_series(1, 20000) g;
VACUUM ANALYZE io_t;
SET max_parallel_workers_per_gather = 0;
EXPLAIN (COSTS OFF) SELECT k FROM io_t ORDER BY k LIMIT 10;
