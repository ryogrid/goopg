create table ws(site int, dsk int, price numeric(7,2), profit numeric(7,2), item int, ord int);
create table wr(dsk int, amt numeric(7,2), loss numeric(7,2), item int, ord int);
create table dd(dsk int, dt date);
insert into ws select g%10, g%100, g, g, g, g from generate_series(1,5000) g;
insert into wr select g%100, g, g, g, g from generate_series(1,2000) g;
insert into dd select g, date '2000-01-01' + g from generate_series(1,100) g;
analyze ws; analyze wr; analyze dd;
explain (costs off) with wsr as (select site, sum(sales_price) s, sum(profit) p, sum(return_amt) r, sum(net_loss) l from (select site as wsite, dsk as date_sk, price as sales_price, profit, cast(0 as decimal(7,2)) as return_amt, cast(0 as decimal(7,2)) as net_loss from ws union all select ws.site, wr.dsk, cast(0 as decimal(7,2)), cast(0 as decimal(7,2)), amt, loss from wr left join ws on wr.item = ws.item and wr.ord = ws.ord) sr, dd, (select distinct site from ws) w where date_sk = dd.dsk and wsite = w.site group by w.site) select * from wsr;
explain (costs off) select w.site, sum(sales_price) s, sum(profit) p, sum(return_amt) r, sum(net_loss) l from (select site as wsite, dsk as date_sk, price as sales_price, profit, cast(0 as decimal(7,2)) as return_amt, cast(0 as decimal(7,2)) as net_loss from ws union all select ws.site, wr.dsk, cast(0 as decimal(7,2)), cast(0 as decimal(7,2)), amt, loss from wr left join ws on wr.item = ws.item and wr.ord = ws.ord) sr, dd, (select distinct site from ws) w where date_sk = dd.dsk and wsite = w.site group by w.site;
explain (costs off) select d2.m, sum(price) from d d2, (select s1.v as price, s1.k as dk, s1.pad as p from s1, d where d.k = s1.k and d.m = 3 union all select s2.v, s2.k, s2.pad from s2, d where d.k = s2.k and d.m = 3) u where d2.k = u.dk group by d2.m;
explain (costs off) select d2.m, sum(price) from (select s1.v as price, s1.k as dk, s1.pad as p from s1, d where d.k = s1.k and d.m = 3 union all select s2.v, s2.k, s2.pad from s2, d where d.k = s2.k and d.m = 3 union all select s1.v, s1.k, s1.pad from s1, d where d.k = s1.k and d.m = 4) u, d d2 where d2.k = u.dk group by d2.m;
set parallel_setup_cost=0; set parallel_tuple_cost=0; set min_parallel_table_scan_size=0;
explain (costs off) select channel, col_name, m, count(*) sales_cnt, sum(price) sales_amt from (
 select 'store' as channel, 's1_pad' col_name, d.m, s1.v price from s1, d where s1.pad is null and s1.k = d.k
 union all select 'web' as channel, 's2_pad' col_name, d.m, s2.v price from s2, d where s2.pad is null and s2.k = d.k
 union all select 'cat' as channel, 's1_k' col_name, d.m, s1.v price from s1, d where s1.k is null and s1.k = d.k) foo
 group by channel, col_name, m order by channel, col_name, m limit 100;
select channel, col_name, m, count(*) sales_cnt, sum(price) sales_amt from (
 select 'store' as channel, 's1_pad' col_name, d.m, s1.v price from s1, d where s1.pad is null and s1.k = d.k
 union all select 'web' as channel, 's2_pad' col_name, d.m, s2.v price from s2, d where s2.pad is null and s2.k = d.k
 union all select 'cat' as channel, 's1_k' col_name, d.m, s1.v price from s1, d where s1.k is null and s1.k = d.k) foo
 group by channel, col_name, m order by channel, col_name, m limit 100;
