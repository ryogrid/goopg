select count(*) from customer c, customer_address ca, customer_demographics
 where c.c_current_addr_sk = ca.ca_address_sk and cd_demo_sk = c.c_current_cdemo_sk;
