-- P0-E14 (1): an INITIALLY DEFERRED constraint trigger fires at COMMIT, not at the statement
create table p0e14_t (id int);
create function p0e14_f() returns trigger language plpgsql as $$
begin raise notice 'fired for %', new.id; return null; end $$;
create constraint trigger p0e14_ct after insert on p0e14_t
  deferrable initially deferred for each row execute function p0e14_f();
begin;
insert into p0e14_t values (1);
select 'after insert, before commit' as marker;   -- PG: no NOTICE yet
commit;                                            -- PG: NOTICE fired for 1 here
drop table p0e14_t;
drop function p0e14_f();
