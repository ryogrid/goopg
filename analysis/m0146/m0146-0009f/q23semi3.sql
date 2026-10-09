SET max_parallel_workers_per_gather=0;
-- E: having count(*) > 0 (keeps HAVING, changes nothing semantically)
EXPLAIN with f as materialized (select i_item_sk item_sk, d_date, count(*) cnt from store_sales, date_dim, item
  where ss_sold_date_sk = d_date_sk and ss_item_sk = i_item_sk and d_year in (2000,2001,2002,2003)
  group by i_item_sk, d_date having count(*) > 0)
select 1 from catalog_sales where cs_item_sk in (select item_sk from f);
-- F: no HAVING, materialized, single ref
EXPLAIN with f as materialized (select i_item_sk item_sk, d_date, count(*) cnt from store_sales, date_dim, item
  where ss_sold_date_sk = d_date_sk and ss_item_sk = i_item_sk and d_year in (2000,2001,2002,2003)
  group by i_item_sk, d_date)
select 1 from catalog_sales where cs_item_sk in (select item_sk from f);
-- G: plain materialized CTE of 4582-ish rows without grouping
EXPLAIN with f as materialized (select ss_item_sk item_sk from store_sales where ss_ticket_number < 3000)
select 1 from catalog_sales where cs_item_sk in (select item_sk from f);
