SET max_parallel_workers_per_gather=0;
EXPLAIN select ws1.ws_order_number from web_sales ws1,web_sales ws2 where ws1.ws_order_number = ws2.ws_order_number and ws1.ws_warehouse_sk <> ws2.ws_warehouse_sk;
EXPLAIN select ws1.ws_order_number from web_sales ws1,web_sales ws2 where ws1.ws_order_number = ws2.ws_order_number;
EXPLAIN select 1 from web_sales ws1,web_sales ws2 where ws1.ws_order_number = ws2.ws_order_number and ws1.ws_warehouse_sk = ws2.ws_warehouse_sk;
select attname, n_distinct, null_frac from pg_stats where tablename='web_sales' and attname in ('ws_order_number','ws_warehouse_sk');
