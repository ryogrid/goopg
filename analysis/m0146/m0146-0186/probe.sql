ANALYZE;
EXPLAIN SELECT d_date_sk FROM date_dim WHERE d_month_seq >= (SELECT 1200) AND d_month_seq <= (SELECT 1203);
EXPLAIN SELECT d_date_sk FROM date_dim WHERE d_month_seq >= 1200 AND d_month_seq <= 1203;
