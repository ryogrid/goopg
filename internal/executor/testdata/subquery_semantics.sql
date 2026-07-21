-- Subquery semantics matrix (gate V1) — oracle-diff probe file.
--
-- Companion to internal/executor/subquery_semantics_test.go. Running this
-- against both engines re-verifies that the expectations pinned in the Go
-- suite are genuinely PostgreSQL 18.3's behaviour and not our reading of the
-- manual:
--
--     scripts/pg-oracle-diff.sh --auto-start internal/executor/testdata/subquery_semantics.sql
--
-- A PASS means goopg matches PG on every probe. At the time of writing several
-- probes FAIL by design — they are the live bugs pinned in the Go suite
-- (see docs/design/correlated-subquery-planning/evidence/review-probes-20260720.md
-- and the `known` entries in the test): the correlated NOT IN vacuous-truth
-- case, the count(col) count bug, the OR-position scalar sublink, `<> ALL`
-- unnesting, and the LIMIT / ungrouped-aggregate EXISTS bodies.
--
-- NOTE: probes of the form `... OR <IN-subquery>` and `NOT (x IN (SELECT ...))`
-- are deliberately absent — at HEAD they send goopg's planner into an unbounded
-- allocation loop (F1). Add them once the Stage 4 (S1a) guard lands.

DROP TABLE IF EXISTS t1;
DROP TABLE IF EXISTS t2;
CREATE TABLE t1 (a int, b int);
CREATE TABLE t2 (a int, b int);
INSERT INTO t1 VALUES (1, 10), (2, 20), (3, 30), (4, NULL);
INSERT INTO t2 VALUES (1, 10), (1, 11), (3, NULL);

-- M1: IN NULL propagation.
SELECT a FROM t1 WHERE b IN (SELECT b FROM t2) ORDER BY a;
SELECT a FROM t1 WHERE b IN (SELECT b FROM t2 WHERE t2.a = 99) ORDER BY a;
SELECT a FROM t1 WHERE b IN (SELECT b FROM t2 WHERE b IS NOT NULL) ORDER BY a;

-- M2: NOT IN NULL propagation, incl. the vacuous NULL NOT IN (empty) case.
SELECT a FROM t1 WHERE b NOT IN (SELECT b FROM t2) ORDER BY a;
SELECT a FROM t1 WHERE b NOT IN (SELECT b FROM t2 WHERE b IS NOT NULL) ORDER BY a;
SELECT a FROM t1 WHERE b NOT IN (SELECT b FROM t2 WHERE t2.a = t1.a) ORDER BY a;

-- M3: EXISTS / NOT EXISTS with NULL correlation values.
SELECT a FROM t1 WHERE EXISTS (SELECT 1 FROM t2 WHERE t2.a = t1.a) ORDER BY a;
SELECT a FROM t1 WHERE NOT EXISTS (SELECT 1 FROM t2 WHERE t2.a = t1.a) ORDER BY a;
SELECT a FROM t1 WHERE EXISTS (SELECT 1 FROM t2 WHERE t2.b = t1.b) ORDER BY a;

-- M4: scalar subquery cardinality (the third raises SQLSTATE 21000).
SELECT a FROM t1 WHERE (SELECT b FROM t2 WHERE t2.a = 99) IS NULL ORDER BY a;
SELECT (SELECT a FROM t2 WHERE b = 11);
SELECT (SELECT a FROM t2);

-- M5: the count bug and its NULL-on-empty siblings.
SELECT a FROM t1 WHERE t1.a > (SELECT count(*) FROM t2 WHERE t2.a = t1.a) ORDER BY a;
SELECT a FROM t1 WHERE t1.a > (SELECT count(b) FROM t2 WHERE t2.a = t1.a) ORDER BY a;
SELECT a FROM t1 WHERE t1.a > (SELECT sum(b) FROM t2 WHERE t2.a = t1.a) ORDER BY a;
SELECT a FROM t1 WHERE t1.a > (SELECT COALESCE(sum(b), 0) FROM t2 WHERE t2.a = t1.a) ORDER BY a;

-- M6: OR-position sublinks must not decorrelate.
SELECT a FROM t1 WHERE a = 2 OR EXISTS (SELECT 1 FROM t2 WHERE t2.a = t1.a) ORDER BY a;
SELECT a FROM t1 WHERE a = 2 OR b > (SELECT sum(x.b) FROM t2 x WHERE x.a = t1.a) ORDER BY a;

-- M7: a sublink in WHERE above a LEFT JOIN applies post-join.
SELECT t1.a FROM t1 LEFT JOIN t2 ON t1.a = t2.a
 WHERE EXISTS (SELECT 1 FROM t2 x WHERE x.a = t1.a) ORDER BY t1.a;

-- M8: Level-2 correlation reaching the outermost query.
SELECT a FROM t1 WHERE EXISTS (
  SELECT 1 FROM t2 WHERE t2.a = t1.a AND EXISTS (
    SELECT 1 FROM t2 y WHERE y.b = t1.b)) ORDER BY a;

-- M9: correlated IN operand safety.
SELECT a FROM t1 WHERE t1.b IN (SELECT y.b FROM t2 y WHERE y.a = t1.a) ORDER BY a;

-- M10: = ANY / <> ALL forms.
SELECT a FROM t1 WHERE b = ANY (SELECT b FROM t2 WHERE b IS NOT NULL) ORDER BY a;
SELECT a FROM t1 WHERE b <> ALL (SELECT b FROM t2 WHERE b IS NOT NULL) ORDER BY a;

-- M11: non-correlated sublink evaluated once.
SELECT a FROM t1 WHERE a > (SELECT min(a) FROM t2) ORDER BY a;

-- M12: EXISTS bodies — LIMIT / DISTINCT are no-ops, an aggregate body is a tautology.
SELECT a FROM t1 WHERE EXISTS (SELECT 1 FROM t2 WHERE t2.a = t1.a LIMIT 1) ORDER BY a;
SELECT a FROM t1 WHERE EXISTS (SELECT DISTINCT b FROM t2 WHERE t2.a = t1.a) ORDER BY a;
SELECT a FROM t1 WHERE EXISTS (SELECT count(*) FROM t2 WHERE t2.a = t1.a) ORDER BY a;

-- M13: a volatile body must not change the result set.
SELECT a FROM t1 WHERE EXISTS (
  SELECT 1 FROM t2 WHERE t2.a = t1.a AND random() < 2) ORDER BY a;

-- M14: non-equi-only correlation (zero equijoin pairs).
SELECT a FROM t1 WHERE EXISTS (SELECT 1 FROM t2 y WHERE y.b > t1.b) ORDER BY a;

-- M15: a nested sublink inside an EXISTS body (the D3.3 shape).
SELECT a FROM t1 WHERE EXISTS (
  SELECT 1 FROM t2 z WHERE z.a = t1.a AND z.b IN (
    SELECT y.b FROM t2 y WHERE y.a = t1.a)) ORDER BY a;

-- M16: residual lifting alongside the equijoin correlation.
SELECT a FROM t1 WHERE t1.b >= (
  SELECT min(y.b) FROM t2 y WHERE y.a = t1.a AND y.b <= t1.b) ORDER BY a;
