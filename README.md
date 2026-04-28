# goopg

goopg is an experimental project that explores whether a coding-agent-driven
Go implementation can reproduce PostgreSQL behavior when PostgreSQL is treated
as the oracle for correctness.

The project focuses on three validation themes:

1. Feasibility of agent-driven implementation:
	can coding agents build and evolve a meaningful PostgreSQL-like server in
	Go while staying behaviorally aligned with upstream PostgreSQL?
2. Performance characteristics under multithreading:
	how do throughput and latency change as execution paths become more
	concurrent?
3. Effects of direct I/O:
	what trade-offs appear when storage paths use direct I/O compared with
	buffered I/O?

This repository is research-oriented and intentionally iterative. It is meant
for experimentation, measurement, and learning, rather than production use.
