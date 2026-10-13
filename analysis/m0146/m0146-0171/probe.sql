set enable_hashjoin = off; set enable_mergejoin = off; set max_parallel_workers_per_gather = 0; set enable_memoize = off;
explain (analyze, costs off, timing off) select count(*) from (select ss_item_sk, ss_customer_sk from store_sales limit 20000) s join catalog_sales cs on cs.cs_item_sk = s.ss_item_sk and cs.cs_bill_customer_sk = s.ss_customer_sk;
