select count(*) from customer c, customer_address ca, customer_demographics
 where c.c_current_addr_sk = ca.ca_address_sk and cd_demo_sk = c.c_current_cdemo_sk
 and (c.c_customer_sk in (select ws_bill_customer_sk from web_sales,date_dim where ws_sold_date_sk = d_date_sk and d_year = 1999 and d_qoy < 4)
   or c.c_customer_sk in (select cs_ship_customer_sk from catalog_sales,date_dim where cs_sold_date_sk = d_date_sk and d_year = 1999 and d_qoy < 4));
