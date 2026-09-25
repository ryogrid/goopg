create table cq (u int4, th int4, h int4, u2 int4);
insert into cq select g, g % 100, g % 10, (g * 7) % 1000 from generate_series(0, 999) g;
create index cq_th on cq (th);
create index cq_u2 on cq (u2);
analyze cq;
