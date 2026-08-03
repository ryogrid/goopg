select d1.d_year+0 as y1, d2.d_year+0 as y2, count(*) as n
from store_sales, date_dim d1, date_dim d2, customer
where ss_item_sk < 40 and ss_sold_date_sk = d1.d_date_sk
  and ss_customer_sk = c_customer_sk
  and c_first_sales_date_sk = d2.d_date_sk
group by 1,2 order by 1,2 limit 5;
