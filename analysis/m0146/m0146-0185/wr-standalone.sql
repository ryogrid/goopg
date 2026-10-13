EXPLAIN select wp_web_page_sk, sum(wr_return_amt) as returns, sum(wr_net_loss) as profit_loss
 from web_returns, date_dim, web_page
 where wr_returned_date_sk = d_date_sk
       and d_date between cast('2000-08-10' as date) and (cast('2000-08-10' as date) +  INTERVAL '30 days')
       and wr_web_page_sk = wp_web_page_sk
 group by wp_web_page_sk ORDER BY wp_web_page_sk;
SET enable_material = off; SET enable_hashjoin = off;
EXPLAIN select wp_web_page_sk, sum(wr_return_amt) as returns, sum(wr_net_loss) as profit_loss
 from web_returns, date_dim, web_page
 where wr_returned_date_sk = d_date_sk
       and d_date between cast('2000-08-10' as date) and (cast('2000-08-10' as date) +  INTERVAL '30 days')
       and wr_web_page_sk = wp_web_page_sk
 group by wp_web_page_sk ORDER BY wp_web_page_sk;
