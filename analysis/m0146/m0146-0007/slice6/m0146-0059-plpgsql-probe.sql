create or replace function m7f() returns text as $f$
declare r1 text; r2 text;
begin
r1 := (with x as (select a, b from ci_t where b < 5) select count(*)::text from x, x x2 where x.a = x2.a);
r2 := (with x as (select b, count(*) c from ci_t group by b) select string_agg(x2.c::text, ',' order by x.b) from x, x x2 where x.b = x2.b);
return r1 || '/' || r2;
end $f$ language plpgsql;
select m7f();
