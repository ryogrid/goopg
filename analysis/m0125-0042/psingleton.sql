select c.c_customer_sk, c.c_current_addr_sk, c.c_current_cdemo_sk
 from customer c, customer_address ca, customer_demographics
 where c.c_current_addr_sk = ca.ca_address_sk and cd_demo_sk = c.c_current_cdemo_sk
 and exists (select * from store_sales,date_dim where c.c_customer_sk = ss_customer_sk and ss_sold_date_sk = d_date_sk and d_year = 1999 and d_qoy < 4)
 and (c.c_customer_sk in (select ca_address_sk from customer_address where ca_address_sk = 351)
   or c.c_customer_sk in (select ca_address_sk from customer_address where ca_address_sk = 351))
 order by 1;
