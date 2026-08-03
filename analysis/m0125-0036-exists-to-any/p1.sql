select count(*) from customer c, customer_address ca, customer_demographics
 where c.c_current_addr_sk = ca.ca_address_sk and cd_demo_sk = c.c_current_cdemo_sk
 and (exists (select * from web_sales,date_dim where c.c_customer_sk = ws_bill_customer_sk and ws_sold_date_sk = d_date_sk and d_year = 1999 and d_qoy < 4)
   or exists (select * from catalog_sales,date_dim where c.c_customer_sk = cs_ship_customer_sk and cs_sold_date_sk = d_date_sk and d_year = 1999 and d_qoy < 4));
