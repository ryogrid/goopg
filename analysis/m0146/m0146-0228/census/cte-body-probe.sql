select c_customer_sk, sum(ss_quantity*ss_sales_price) ssales
from store_sales, customer
where ss_customer_sk = c_customer_sk
group by c_customer_sk
having sum(ss_quantity*ss_sales_price) > 1000000;
