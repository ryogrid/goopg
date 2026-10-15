DROP TABLE IF EXISTS h;
CREATE TABLE h (id int PRIMARY KEY, a int, b char(15), c int, d int) WITH (autovacuum_enabled = off);
\copy h FROM 'tmp/m0204-h.tsv'
SELECT relpages, relallvisible FROM pg_class WHERE relname = 'h';
VACUUM h;
SELECT relpages, relallvisible FROM pg_class WHERE relname = 'h';
BEGIN;
INSERT INTO h SELECT g, 0, 'z', 0, 0 FROM generate_series(100001, 101000) g;
ROLLBACK;
SET enable_seqscan = off; SET enable_bitmapscan = off;
EXPLAIN (COSTS OFF) SELECT count(*) FROM h WHERE id > 100000;
SELECT count(*) AS ios_count FROM h WHERE id > 100000;
SET enable_indexonlyscan = off;
SELECT count(*) AS indexscan_count FROM h WHERE id > 100000;
CREATE TABLE c5 (id int, a int, b text, c int, d int);
BEGIN;
\copy c5 FROM 'tmp/m0204-h2.tsv'
SELECT count(*) AS in_tx FROM c5;
ROLLBACK;
SELECT count(*) AS after_rollback_seq FROM c5;
BEGIN;
COPY c5 FROM STDIN;
1	1	q	1	1
\.
ROLLBACK;
SELECT count(*) AS after_rollback2 FROM c5;
BEGIN;
INSERT INTO c5 VALUES (9,9,'x',9,9);
ROLLBACK;
SELECT count(*) AS after_insert_rollback FROM c5;
DROP TABLE IF EXISTS t;
CREATE TABLE t (id int PRIMARY KEY, pad text) WITH (autovacuum_enabled = off);
INSERT INTO t SELECT g, repeat('x', 200) FROM generate_series(1, 20000) g;
DELETE FROM t WHERE id > 2000;
VACUUM t;
VACUUM t;
SELECT relpages, relallvisible, pg_relation_size('t')/8192 AS file_pages FROM pg_class WHERE relname = 't';
BEGIN;
INSERT INTO t SELECT g, repeat('y', 200) FROM generate_series(100001, 100500) g;
ROLLBACK;
SET enable_seqscan = off; SET enable_bitmapscan = off;
EXPLAIN (COSTS OFF) SELECT count(*) FROM t WHERE id > 100000;
SELECT count(*) AS ios_count FROM t WHERE id > 100000;
SET enable_indexonlyscan = off;
SELECT count(*) AS indexscan_count FROM t WHERE id > 100000;
