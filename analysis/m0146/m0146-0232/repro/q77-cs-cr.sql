explain with cs as
 (select cs_call_center_sk, sum(cs_ext_sales_price) as sales, sum(cs_net_profit) as profit
 from catalog_sales, date_dim
 where cs_sold_date_sk = d_date_sk
       and d_date between cast('2000-08-10' as date) and (cast('2000-08-10' as date) +  INTERVAL '30 days')
 group by cs_call_center_sk),
 cr as
 (select cr_call_center_sk, sum(cr_return_amount) as returns, sum(cr_net_loss) as profit_loss
 from catalog_returns, date_dim
 where cr_returned_date_sk = d_date_sk
       and d_date between cast('2000-08-10' as date) and (cast('2000-08-10' as date) +  INTERVAL '30 days')
 group by cr_call_center_sk)
select cs_call_center_sk, sales, returns, profit - profit_loss from cs, cr;
