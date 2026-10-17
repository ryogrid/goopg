\echo === rollup_no_state
EXPLAIN (COSTS OFF) select s_county, count(*) from store_sales, date_dim d1, store where d1.d_month_seq between 1220 and 1220+11 and d1.d_date_sk = ss_sold_date_sk and s_store_sk = ss_store_sk and s_state in ( select s_state
               from  (select s_state as s_state,
 			    rank() over ( partition by s_state order by sum(ss_net_profit) desc) as ranking
                      from   store_sales, store, date_dim
                      where  d_month_seq between 1220 and 1220+11
 			    and d_date_sk = ss_sold_date_sk
 			    and s_store_sk  = ss_store_sk
                      group by s_state
                     ) tmp1 
               where ranking <= 5
             )
  group by rollup(s_county);
\echo === plain_no_state
EXPLAIN (COSTS OFF) select s_county, count(*) from store_sales, date_dim d1, store where d1.d_month_seq between 1220 and 1220+11 and d1.d_date_sk = ss_sold_date_sk and s_store_sk = ss_store_sk and s_state in ( select s_state
               from  (select s_state as s_state,
 			    rank() over ( partition by s_state order by sum(ss_net_profit) desc) as ranking
                      from   store_sales, store, date_dim
                      where  d_month_seq between 1220 and 1220+11
 			    and d_date_sk = ss_sold_date_sk
 			    and s_store_sk  = ss_store_sk
                      group by s_state
                     ) tmp1 
               where ranking <= 5
             )
  group by s_county;
