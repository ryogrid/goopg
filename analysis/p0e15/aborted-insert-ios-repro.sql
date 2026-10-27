-- P0-E15: an index-only scan returns a rolled-back INSERT after VACUUM.
CREATE TABLE p0e15 (id int);
CREATE INDEX p0e15_id ON p0e15 (id);
INSERT INTO p0e15 SELECT g FROM generate_series(1, 2000) g;
BEGIN;
INSERT INTO p0e15 VALUES (99999);
ROLLBACK;
VACUUM p0e15;
SET enable_seqscan = off;
SET enable_bitmapscan = off;
EXPLAIN (COSTS OFF) SELECT id FROM p0e15 WHERE id = 99999;
SELECT id FROM p0e15 WHERE id = 99999;
RESET enable_seqscan;
RESET enable_bitmapscan;
SELECT count(*) FROM p0e15;
SELECT count(*) FROM p0e15 WHERE id = 99999;
DROP TABLE p0e15;
