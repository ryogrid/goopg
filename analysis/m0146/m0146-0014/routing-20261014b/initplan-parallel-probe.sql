EXPLAIN (COSTS OFF) SELECT ss_item_sk, count(*) FROM store_sales WHERE ss_net_paid > (SELECT avg(ss_net_paid) FROM store_sales) GROUP BY ss_item_sk;
EXPLAIN (COSTS OFF) SELECT ss_item_sk, count(*) FROM store_sales WHERE ss_net_paid > 100 GROUP BY ss_item_sk;
