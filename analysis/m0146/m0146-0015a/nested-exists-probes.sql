select sum((select count(*) from cq d where d.th = a.th)) from cq a;
select sum((select count(*) from cq d where d.th > a.th)) from cq a where a.u < 50;
select count(*) from cq a where a.u < 300 and exists (select 1 from cq b where b.th = a.th and exists (select 1 from cq c where c.u2 = a.u2 + 1 and c.h = b.h));
select count(*) from cq a where a.u < 300 and not exists (select 1 from cq b where b.th = a.th and b.u <> a.u and exists (select 1 from cq c where c.u2 = a.u2 + 3 and c.h = b.h));
select count(*) from cq a where exists (select 1 from cq b where b.th = a.th and not exists (select 1 from cq d where a.th = d.th));
select count(*) from cq a where a.u < 300 and exists (select 1 from cq b where b.th = a.th and exists (select 1 from cq c where c.u2 = a.u2 + 70 and c.h = b.h));
select count(*) from cq a where a.u < 300 and exists (select 1 from cq b where b.th = a.th and exists (select 1 from cq c where c.u2 = a.u2 + 7 and c.h = b.h));
select count(*) from cq a where a.u < 300 and not exists (select 1 from cq b where b.th = a.th and b.u <> a.u and exists (select 1 from cq c where c.u2 = a.u2 + 70 and c.h = b.h));
select count(*) from cq a where a.u < 300 and exists (select 1 from cq b where b.h = a.h and b.u > a.u and exists (select 1 from cq c where c.u = a.u + 5 and c.th = b.th));
