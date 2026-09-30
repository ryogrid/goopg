SET max_parallel_workers_per_gather=0;
EXPLAIN WITH ss AS (SELECT d_year AS ss_sold_year, ss_item_sk, ss_customer_sk, sum(ss_quantity) q
  FROM store_sales LEFT JOIN store_returns ON sr_ticket_number=ss_ticket_number AND ss_item_sk=sr_item_sk
  JOIN date_dim ON ss_sold_date_sk=d_date_sk
  WHERE sr_ticket_number IS NULL GROUP BY d_year, ss_item_sk, ss_customer_sk)
SELECT * FROM ss WHERE ss_sold_year=1998;
EXPLAIN SELECT * FROM (SELECT d_year AS ss_sold_year, ss_item_sk, ss_customer_sk, sum(ss_quantity) q
  FROM store_sales JOIN date_dim ON ss_sold_date_sk=d_date_sk
  GROUP BY d_year, ss_item_sk, ss_customer_sk) s WHERE ss_sold_year=1998;
