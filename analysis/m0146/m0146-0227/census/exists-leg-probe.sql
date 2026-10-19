EXPLAIN select c.c_birth_year from customer c
 where exists (select * from store_sales, date_dim where c.c_customer_sk = ss_customer_sk and ss_sold_date_sk = d_date_sk and d_year = 2002 and d_moy between 1 and 3);
