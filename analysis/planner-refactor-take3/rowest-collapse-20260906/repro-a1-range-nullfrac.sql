DROP TABLE IF EXISTS nn; DROP TABLE IF EXISTS nz;
CREATE TABLE nn (v integer);   -- no nulls
CREATE TABLE nz (v integer);   -- 4.4% nulls, same non-null distribution
INSERT INTO nn SELECT (i % 1000) + 1 FROM generate_series(1,500000) i;
INSERT INTO nz SELECT CASE WHEN i % 1000 < 44 THEN NULL ELSE (i % 1000) + 1 END FROM generate_series(1,500000) i;
ANALYZE nn; ANALYZE nz;
\echo '== nn (nullfrac 0): v BETWEEN 100 AND 149  (true 25000 rows)'
EXPLAIN SELECT 1 FROM nn WHERE v between 100 and 149;
EXPLAIN SELECT 1 FROM nn WHERE v >= 100;
EXPLAIN SELECT 1 FROM nn WHERE v <= 149;
\echo '== nz (nullfrac 0.044): same predicate, true 25000 rows'
EXPLAIN SELECT 1 FROM nz WHERE v between 100 and 149;
EXPLAIN SELECT 1 FROM nz WHERE v >= 100;
EXPLAIN SELECT 1 FROM nz WHERE v <= 149;
\echo '== nz narrow band, true 5000 rows (band < nullfrac) -> expect the 1e-10 slam'
EXPLAIN SELECT 1 FROM nz WHERE v between 100 and 109;
EXPLAIN SELECT 1 FROM nn WHERE v between 100 and 109;
\echo '== actuals'
SELECT (SELECT count(*) FROM nn WHERE v between 100 and 149) nn149,
       (SELECT count(*) FROM nz WHERE v between 100 and 149) nz149,
       (SELECT count(*) FROM nz WHERE v between 100 and 109) nz109,
       (SELECT count(*) FROM nz WHERE v IS NULL) nznull;
