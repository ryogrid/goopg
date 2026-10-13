EXPLAIN (COSTS OFF) WITH a AS (SELECT k AS ak, sum(v) AS sa FROM f1 GROUP BY k),
     b AS (SELECT k AS bk, sum(w) AS sb FROM f2 GROUP BY k)
SELECT a.ak, a.sa FROM a JOIN b ON a.ak = b.bk WHERE a.sa > 0.9 * b.sb ORDER BY 1 LIMIT 5;
