DROP TABLE IF EXISTS r65h; DROP TABLE IF EXISTS r65n;
CREATE TABLE r65h (partkey int, supplycost numeric, availqty int); CREATE TABLE r65n (x int, y numeric);
EXPLAIN (COSTS OFF) SELECT partkey, sum(supplycost*availqty) AS s FROM r65h GROUP BY partkey HAVING sum(supplycost*availqty) > (SELECT sum(x) FROM r65n) ORDER BY s DESC;
EXPLAIN (COSTS OFF) SELECT * FROM r65h WHERE availqty > (SELECT avg(y) FROM r65n);
EXPLAIN (COSTS OFF) SELECT * FROM r65h WHERE supplycost > (SELECT max(x) FROM r65n);
DROP TABLE r65h; DROP TABLE r65n;
