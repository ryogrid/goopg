DROP TABLE IF EXISTS fact2;
CREATE TABLE fact2 (d_id integer, amt integer);
INSERT INTO fact2 SELECT CASE WHEN i % 10 = 0 THEN 99 ELSE (i % 5) + 1 END, i FROM generate_series(1,500000) i;
ANALYZE fact2;
\echo '### LEFT JOIN + IS NULL  (true anti-join output = 50000)'
EXPLAIN SELECT 1 FROM fact2 f LEFT JOIN dim d ON f.d_id = d.id WHERE d.id IS NULL;
\echo '### NOT EXISTS spelling of the same thing'
EXPLAIN SELECT 1 FROM fact2 f WHERE NOT EXISTS (SELECT 1 FROM dim d WHERE d.id = f.d_id);
\echo '### actual'
SELECT count(*) FROM fact2 f LEFT JOIN dim d ON f.d_id = d.id WHERE d.id IS NULL;
