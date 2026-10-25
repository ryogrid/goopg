-- P0-E12 repro: a computed text group key (substr over an arena-backed text column) is retained by a hash
-- aggregate whose input scan borrows its row; the key's bytes alias the scan's per-page arena and are overwritten.
-- Run on a scratch database (it creates a table). Expected: 60000 distinct keys, each count 1
-- (analysis/p0e12/expected.out, computed independently from the generator).
CREATE TABLE gq (id int, t text, pad text);
INSERT INTO gq SELECT x, 'v' || x || '-' || md5(x::text), repeat('z', 200) FROM generate_series(1, 60000) x;
ANALYZE gq;
SET max_parallel_workers_per_gather = 0;
SELECT substr(t, 2, 6), count(*) FROM gq GROUP BY 1;
