SET max_parallel_workers_per_gather=0;
EXPLAIN with frequent_ss_items as
 (select substr(i_item_desc,1,30) itemdesc,i_item_sk item_sk,d_date solddate,count(*) cnt
  from store_sales ,date_dim ,item
  where ss_sold_date_sk = d_date_sk and ss_item_sk = i_item_sk and d_year in (2000,2001,2002,2003)
  group by substr(i_item_desc,1,30),i_item_sk,d_date having count(*) >4)
select cs_quantity from catalog_sales, date_dim
 where d_year = 2000 and d_moy = 7 and cs_sold_date_sk = d_date_sk
   and cs_item_sk in (select item_sk from frequent_ss_items)
   and cs_item_sk in (select item_sk from frequent_ss_items);
EXPLAIN with frequent_ss_items as
 (select substr(i_item_desc,1,30) itemdesc,i_item_sk item_sk,d_date solddate,count(*) cnt
  from store_sales ,date_dim ,item
  where ss_sold_date_sk = d_date_sk and ss_item_sk = i_item_sk and d_year in (2000,2001,2002,2003)
  group by substr(i_item_desc,1,30),i_item_sk,d_date having count(*) >4)
select cs_quantity from catalog_sales
 where cs_item_sk in (select item_sk from frequent_ss_items)
   and cs_item_sk in (select item_sk from frequent_ss_items);
