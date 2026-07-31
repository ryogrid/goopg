select d1.d_year as y1, d2.d_year as y2, d3.d_year as y3, count(*) as n
from store_sales, date_dim d1, date_dim d2, date_dim d3, customer
where ss_item_sk < 40 and ss_sold_date_sk = d1.d_date_sk
  and ss_customer_sk = c_customer_sk
  and c_first_sales_date_sk = d2.d_date_sk
  and c_first_shipto_date_sk = d3.d_date_sk
group by 1,2,3 order by 1,2,3 limit 8;
