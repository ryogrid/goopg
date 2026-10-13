EXPLAIN select wp_web_page_sk, sum(ws_ext_sales_price) as sales, sum(ws_net_profit) as profit
 from web_sales, date_dim, web_page
 where ws_sold_date_sk = d_date_sk
       and d_date between cast('2000-08-10' as date) and (cast('2000-08-10' as date) +  INTERVAL '30 days')
       and ws_web_page_sk = wp_web_page_sk
 group by wp_web_page_sk ORDER BY wp_web_page_sk;
