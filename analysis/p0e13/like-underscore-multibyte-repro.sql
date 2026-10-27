-- P0-E13: LIKE '_' must match one CHARACTER, not one byte, under a UTF-8
-- server encoding (PG MatchText, like_match.c, NextChar = pg_mblen).
select 'é' like '_'    as one_char,       -- PG t
       'é' like '__'   as two_underscores, -- PG f
       'aéb' like 'a_b' as middle,         -- PG t
       'é' ilike '_'   as ilike_one;      -- PG t
