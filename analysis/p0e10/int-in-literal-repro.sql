-- P0-E10 repro: an integer operand IN a list of unknown-typed literals.
-- PG coerces each literal to int4 at parse time (transformAExprIn), so
-- '02000' and ' 2001' are 2000 and 2001. Read-only; no DDL.
SELECT count(*) AS in_padded FROM (VALUES (2000), (2001), (2002)) v(y) WHERE y IN ('02000', ' 2001');
SELECT count(*) AS in_plain FROM (VALUES (2000), (2001), (2002)) v(y) WHERE y IN ('2000', '2001');
SELECT count(*) AS not_in_padded FROM (VALUES (2000), (2001), (2002)) v(y) WHERE y NOT IN ('02000', '+2001');
SELECT count(*) AS eq_padded FROM (VALUES (2000), (2001), (2002)) v(y) WHERE y = '02000';
SELECT count(*) AS tpcds_in FROM date_dim WHERE d_year IN ('02000', ' 2001');
