create table o(id int);
create table i(k int primary key, v int, pad text);
insert into o select (g * 7919) % 500000 + 1 from generate_series(1, 200000) g;
insert into i select g, g % 1000, repeat('x', 40) from generate_series(1, 500000) g;
vacuum analyze o; vacuum analyze i;
