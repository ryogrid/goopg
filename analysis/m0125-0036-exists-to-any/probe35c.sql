select count(*) from customer c where
 exists (select * from store_sales,date_dim where c.c_customer_sk = ss_customer_sk and ss_sold_date_sk = d_date_sk and d_year = 1999 and d_qoy < 4)
 and (exists (select * from web_sales,date_dim where c.c_customer_sk = ws_bill_customer_sk and ws_sold_date_sk = d_date_sk and d_year = 1999 and d_qoy < 4)
   or exists (select * from catalog_sales,date_dim where c.c_customer_sk = cs_ship_customer_sk and cs_sold_date_sk = d_date_sk and d_year = 1999 and d_qoy < 4));
