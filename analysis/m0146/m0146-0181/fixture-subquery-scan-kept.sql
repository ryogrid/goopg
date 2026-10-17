DROP TABLE IF EXISTS f1, f2;
CREATE TABLE f1 (k int, pad text, v numeric);
CREATE TABLE f2 (k int, pad text, w numeric);
INSERT INTO f1 SELECT g % 100, 'x', g FROM generate_series(1, 5000) g;
INSERT INTO f2 SELECT g % 100, 'y', g FROM generate_series(1, 5000) g;
ANALYZE f1; ANALYZE f2;
EXPLAIN (COSTS OFF) WITH a AS (SELECT pad, k AS ak, sum(v) AS sa FROM f1 GROUP BY pad, k),
     b AS (SELECT pad, k AS bk, sum(w) AS sb FROM f2 GROUP BY pad, k)
SELECT a.ak, a.sa FROM a JOIN b ON a.ak = b.bk WHERE a.sa > 0.9 * b.sb ORDER BY 1 LIMIT 5;
