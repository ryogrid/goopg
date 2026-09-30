SET max_parallel_workers_per_gather=0;
-- A: 3-key grouped CTE (as Q23), referenced twice
EXPLAIN with f as (select i_item_sk item_sk, d_date, count(*) cnt from store_sales, date_dim, item
  where ss_sold_date_sk = d_date_sk and ss_item_sk = i_item_sk and d_year in (2000,2001,2002,2003)
  group by i_item_sk, d_date having count(*) > 4)
select 1 from catalog_sales where cs_item_sk in (select item_sk from f) and cs_item_sk in (select item_sk from f);
-- B: same without HAVING
EXPLAIN with f as (select i_item_sk item_sk, d_date, count(*) cnt from store_sales, date_dim, item
  where ss_sold_date_sk = d_date_sk and ss_item_sk = i_item_sk and d_year in (2000,2001,2002,2003)
  group by i_item_sk, d_date)
select 1 from catalog_sales where cs_item_sk in (select item_sk from f) and cs_item_sk in (select item_sk from f);
-- C: MATERIALIZED plain (ungrouped) CTE over item
EXPLAIN with f as materialized (select i_item_sk item_sk from item where i_item_sk < 5000)
select 1 from catalog_sales where cs_item_sk in (select item_sk from f);
-- D: 3-key grouped CTE single ref, NOT inlined (grouped)
EXPLAIN with f as materialized (select i_item_sk item_sk, d_date, count(*) cnt from store_sales, date_dim, item
  where ss_sold_date_sk = d_date_sk and ss_item_sk = i_item_sk and d_year in (2000,2001,2002,2003)
  group by i_item_sk, d_date having count(*) > 4)
select 1 from catalog_sales where cs_item_sk in (select item_sk from f);
