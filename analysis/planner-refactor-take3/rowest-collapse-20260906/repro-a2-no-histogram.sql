DROP TABLE IF EXISTS q100; DROP TABLE IF EXISTS q500;
CREATE TABLE q100 (v integer);
CREATE TABLE q500 (v integer);
INSERT INTO q100 SELECT (i % 100) + 1 FROM generate_series(1,200000) i;
INSERT INTO q500 SELECT (i % 500) + 1 FROM generate_series(1,200000) i;
ANALYZE q100; ANALYZE q500;
\echo '== q100 (100 distinct: MCV list should hold all, histogram empty)'
EXPLAIN SELECT * FROM q100 WHERE v between 1 and 5;
EXPLAIN SELECT * FROM q100 WHERE v <= 5;
EXPLAIN SELECT * FROM q100 WHERE v >= 1;
\echo '== q500 (500 distinct: histogram present)'
EXPLAIN SELECT * FROM q500 WHERE v between 1 and 25;
EXPLAIN SELECT * FROM q500 WHERE v <= 25;
EXPLAIN SELECT * FROM q500 WHERE v >= 1;
\echo '== actuals'
SELECT (SELECT count(*) FROM q100 WHERE v between 1 and 5) a100, (SELECT count(*) FROM q500 WHERE v between 1 and 25) a500;
