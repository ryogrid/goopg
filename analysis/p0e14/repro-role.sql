-- P0-E14 (2): SET ROLE inside a transaction that aborts must be undone by ROLLBACK
create role p0e14_r;
begin;
set role p0e14_r;
select 1/0;
rollback;
select current_user = session_user as restored;   -- PG: t
begin;
set role p0e14_r;
rollback;
select current_user = session_user as restored2;  -- PG: t
reset role;
drop role p0e14_r;
