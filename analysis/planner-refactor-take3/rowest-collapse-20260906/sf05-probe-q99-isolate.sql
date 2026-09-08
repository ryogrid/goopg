\pset pager off
\echo '### P1: Q99 body, plain cols, NO order/limit  (b3 repeat)'
EXPLAIN SELECT w_warehouse_name, sm_type, cc_name, count(*) FROM catalog_sales, warehouse, ship_mode, call_center, date_dim
 WHERE d_month_seq between 1194 and 1194+11 AND cs_ship_date_sk=d_date_sk AND cs_warehouse_sk=w_warehouse_sk
   AND cs_ship_mode_sk=sm_ship_mode_sk AND cs_call_center_sk=cc_call_center_sk
 GROUP BY w_warehouse_name, sm_type, cc_name;
\echo '### P2: same + substr'
EXPLAIN SELECT substr(w_warehouse_name,1,20), sm_type, cc_name, count(*) FROM catalog_sales, warehouse, ship_mode, call_center, date_dim
 WHERE d_month_seq between 1194 and 1194+11 AND cs_ship_date_sk=d_date_sk AND cs_warehouse_sk=w_warehouse_sk
   AND cs_ship_mode_sk=sm_ship_mode_sk AND cs_call_center_sk=cc_call_center_sk
 GROUP BY substr(w_warehouse_name,1,20), sm_type, cc_name;
\echo '### P3: plain cols + ORDER BY + LIMIT'
EXPLAIN SELECT w_warehouse_name, sm_type, cc_name, count(*) FROM catalog_sales, warehouse, ship_mode, call_center, date_dim
 WHERE d_month_seq between 1194 and 1194+11 AND cs_ship_date_sk=d_date_sk AND cs_warehouse_sk=w_warehouse_sk
   AND cs_ship_mode_sk=sm_ship_mode_sk AND cs_call_center_sk=cc_call_center_sk
 GROUP BY w_warehouse_name, sm_type, cc_name ORDER BY w_warehouse_name, sm_type, cc_name LIMIT 100;
\echo '### P4: substr + ORDER BY + LIMIT (= Q99 minus the 5 sum(case))'
EXPLAIN SELECT substr(w_warehouse_name,1,20), sm_type, cc_name, count(*) FROM catalog_sales, warehouse, ship_mode, call_center, date_dim
 WHERE d_month_seq between 1194 and 1194+11 AND cs_ship_date_sk=d_date_sk AND cs_warehouse_sk=w_warehouse_sk
   AND cs_ship_mode_sk=sm_ship_mode_sk AND cs_call_center_sk=cc_call_center_sk
 GROUP BY substr(w_warehouse_name,1,20), sm_type, cc_name ORDER BY substr(w_warehouse_name,1,20), sm_type, cc_name LIMIT 100;
\echo '### P5: Q99 verbatim but enable_memoize=off'
SET enable_memoize=off;
EXPLAIN select 
   substr(w_warehouse_name,1,20)
  ,sm_type
  ,cc_name
  ,sum(case when (cs_ship_date_sk - cs_sold_date_sk <= 30 ) then 1 else 0 end)  as "30 days" 
  ,sum(case when (cs_ship_date_sk - cs_sold_date_sk > 30) and 
                 (cs_ship_date_sk - cs_sold_date_sk <= 60) then 1 else 0 end )  as "31-INTERVAL '60 days'" 
  ,sum(case when (cs_ship_date_sk - cs_sold_date_sk > 60) and 
                 (cs_ship_date_sk - cs_sold_date_sk <= 90) then 1 else 0 end)  as "61-INTERVAL '90 days'" 
  ,sum(case when (cs_ship_date_sk - cs_sold_date_sk > 90) and
                 (cs_ship_date_sk - cs_sold_date_sk <= 120) then 1 else 0 end)  as "91-INTERVAL '120 days'" 
  ,sum(case when (cs_ship_date_sk - cs_sold_date_sk  > 120) then 1 else 0 end)  as ">120 days" 
from
   catalog_sales
  ,warehouse
  ,ship_mode
  ,call_center
  ,date_dim
where
    d_month_seq between 1194 and 1194 + 11
and cs_ship_date_sk   = d_date_sk
and cs_warehouse_sk   = w_warehouse_sk
and cs_ship_mode_sk   = sm_ship_mode_sk
and cs_call_center_sk = cc_call_center_sk
group by
   substr(w_warehouse_name,1,20)
  ,sm_type
  ,cc_name
order by substr(w_warehouse_name,1,20)
        ,sm_type
        ,cc_name
limit 100;
SET enable_memoize=on;
\echo '### P6: Q99 verbatim but enable_nestloop=off'
SET enable_nestloop=off;
EXPLAIN select 
   substr(w_warehouse_name,1,20)
  ,sm_type
  ,cc_name
  ,sum(case when (cs_ship_date_sk - cs_sold_date_sk <= 30 ) then 1 else 0 end)  as "30 days" 
  ,sum(case when (cs_ship_date_sk - cs_sold_date_sk > 30) and 
                 (cs_ship_date_sk - cs_sold_date_sk <= 60) then 1 else 0 end )  as "31-INTERVAL '60 days'" 
  ,sum(case when (cs_ship_date_sk - cs_sold_date_sk > 60) and 
                 (cs_ship_date_sk - cs_sold_date_sk <= 90) then 1 else 0 end)  as "61-INTERVAL '90 days'" 
  ,sum(case when (cs_ship_date_sk - cs_sold_date_sk > 90) and
                 (cs_ship_date_sk - cs_sold_date_sk <= 120) then 1 else 0 end)  as "91-INTERVAL '120 days'" 
  ,sum(case when (cs_ship_date_sk - cs_sold_date_sk  > 120) then 1 else 0 end)  as ">120 days" 
from
   catalog_sales
  ,warehouse
  ,ship_mode
  ,call_center
  ,date_dim
where
    d_month_seq between 1194 and 1194 + 11
and cs_ship_date_sk   = d_date_sk
and cs_warehouse_sk   = w_warehouse_sk
and cs_ship_mode_sk   = sm_ship_mode_sk
and cs_call_center_sk = cc_call_center_sk
group by
   substr(w_warehouse_name,1,20)
  ,sm_type
  ,cc_name
order by substr(w_warehouse_name,1,20)
        ,sm_type
        ,cc_name
limit 100;
SET enable_nestloop=on;
