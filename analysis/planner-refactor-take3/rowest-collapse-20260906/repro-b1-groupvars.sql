DROP TABLE IF EXISTS fact; DROP TABLE IF EXISTS dim; DROP TABLE IF EXISTS dim2;
CREATE TABLE dim  (id integer, name text);
CREATE TABLE dim2 (id integer, kind text);
CREATE TABLE fact (d_id integer, d2_id integer, amt integer);
INSERT INTO dim  SELECT i, 'name' || i FROM generate_series(1,5) i;
INSERT INTO dim2 SELECT i, 'kind' || i FROM generate_series(1,6) i;
INSERT INTO fact SELECT (i % 5) + 1, (i % 6) + 1, i FROM generate_series(1,500000) i;
ANALYZE dim; ANALYZE dim2; ANALYZE fact;
\echo '### G1 group by dim column across a 2-way join'
EXPLAIN SELECT d.name, count(*) FROM fact f, dim d WHERE f.d_id = d.id GROUP BY d.name;
\echo '### G2 group by two dim columns across a 3-way join'
EXPLAIN SELECT d.name, e.kind, count(*) FROM fact f, dim d, dim2 e WHERE f.d_id=d.id AND f.d2_id=e.id GROUP BY d.name, e.kind;
\echo '### G3 same, hashjoin disabled (force nested loop)'
SET enable_hashjoin = off;
EXPLAIN SELECT d.name, e.kind, count(*) FROM fact f, dim d, dim2 e WHERE f.d_id=d.id AND f.d2_id=e.id GROUP BY d.name, e.kind;
SET enable_hashjoin = on;
\echo '### G4 group by fact column across the join'
EXPLAIN SELECT f.d_id, count(*) FROM fact f, dim d WHERE f.d_id = d.id GROUP BY f.d_id;
\echo '### G5 group over UNION ALL'
EXPLAIN SELECT k, count(*) FROM (SELECT name k FROM dim UNION ALL SELECT kind FROM dim2) s GROUP BY k;
