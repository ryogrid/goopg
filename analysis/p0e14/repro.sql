
-- Test who runs deferred trigger functions

-- setup
create role regress_caller;
create role regress_fn_owner;
create function whoami() returns trigger language plpgsql
as $$
begin
  raise notice 'I am %', current_user;
  return null;
end;
$$;
alter function whoami() owner to regress_fn_owner;

create table defer_trig (id integer);
grant insert on defer_trig to public;
create constraint trigger whoami after insert on defer_trig
  deferrable initially deferred
  for each row
  execute function whoami();

-- deferred triggers must run as the user that queued the trigger
begin;
set role regress_caller;
insert into defer_trig values (1);
reset role;
set role regress_fn_owner;
insert into defer_trig values (2);
reset role;
commit;

-- security definer functions override the user who queued the trigger
alter function whoami() security definer;
begin;
set role regress_caller;
insert into defer_trig values (3);
reset role;
commit;
alter function whoami() security invoker;

-- make sure the current user is restored after error
create or replace function whoami() returns trigger language plpgsql
as $$
begin
  raise notice 'I am %', current_user;
  perform 1 / 0;
  return null;
end;
$$;

begin;
set role regress_caller;
insert into defer_trig values (4);
reset role;
commit;  -- error expected
select current_user = session_user;
select id from defer_trig order by id;

-- clean up
drop table defer_trig;
drop function whoami();
drop role regress_fn_owner;
drop role regress_caller;
