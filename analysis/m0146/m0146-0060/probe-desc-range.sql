drop table if exists dsc;
CREATE TABLE dsc (a int, b int, c int);
INSERT INTO dsc SELECT g % 100, g % 13, g FROM generate_series(1, 3000) g;
CREATE INDEX dsc_a ON dsc (a DESC);
VACUUM dsc; ANALYZE dsc;
EXPLAIN SELECT a, c FROM dsc WHERE a > 97;
EXPLAIN SELECT a, c FROM dsc WHERE a BETWEEN 3 AND 4;
EXPLAIN SELECT a FROM dsc WHERE a > 97;
