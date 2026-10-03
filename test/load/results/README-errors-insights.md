# Errors insights: 31-day ClickHouse read validation

The opt-in `TestErrorsPerformance31Days` in
`ee/observe/errors_performance_test.go` exercises the actual Explorer methods.
It records each SQL statement, runs `EXPLAIN indexes=1` with its bound arguments,
and reports `system.query_log` duration, read rows, read bytes and peak memory.
Use a disposable database: fixture applications remain available for inspection.

```sh
TEST_DATABASE_URL='postgres://USER:PASSWORD@HOST:5432/DATABASE?sslmode=disable' \
TEST_CLICKHOUSE_URL='clickhouse://USER:PASSWORD@HOST:9000/DATABASE' \
OBSERVE_ERRORS_PERF_ROWS=1000000 \
go test ./ee/observe -run '^TestErrorsPerformance31Days$' -count=1 -v
```

The test requires permission to flush and read the query log to collect server
metrics. When unavailable, correctness, query counts and EXPLAIN still run, and
the missing server metrics are reported explicitly. Ordinary test runs skip this
load fixture unless `OBSERVE_ERRORS_PERF_ROWS` is set.

## Measured workload

Measured on 2026-10-03 against local ClickHouse 25.3.14.14 with `max_threads=5`.
The server's `max_memory_usage` was 0; application reads have a 30-second context
deadline. No schema migration or additional index was introduced.

The fixture spans 2026-08-15 through 2026-09-15, crossing two monthly partitions:

- 1,000,000 distinct logs, 100,000 of which are errors.
- 1,100,167 stored rows after approximately 10% SDK retries.
- 100 error groups shared across 16 updates, 20,000 installations in the full
  log stream and 2,000 installations with errors.
- Two platforms, four runtimes, twelve models, OS variants and mixed fatality.
- Other test applications were present in the same database, exercising
  application pruning.

This is a synthetic workload with short stack payloads and uniform events. The
figures are single-request observations, without application response-cache hits;
they are not production percentiles, cold-disk results or concurrency guarantees.
The measured Explorer had no PostgreSQL handle: optional identity-filter lookups
and the single update-label enrichment query are excluded from these timings.

| Request | ClickHouse queries | Wall time | Rows read, summed | Bytes read, summed | Largest query peak memory |
| --- | ---: | ---: | ---: | ---: | ---: |
| List, 1 group, no series | 1 | 544 ms | 2,202,194 | 243,135,407 | 149,379,593 B |
| List, 100 groups, no series | 1 | 485 ms | 2,202,194 | 243,135,407 | 148,226,809 B |
| List, 1 group, series | 2 | 685 ms | 4,408,108 | 486,492,254 | 143,998,466 B |
| List, 100 groups, series | 2 | 1,098 ms | 4,408,108 | 486,492,254 | 161,290,045 B |
| Detail, 1 occurrence | 2 | 663 ms | 5,511,995 | 516,090,451 | 36,420,633 B |
| Detail, 100 occurrences | 2 | 552 ms | 5,511,995 | 550,010,744 | 31,462,023 B |
| Detail, old fallback ID, 100 occurrences | 3 | 651 ms | 5,512,511 | 550,040,632 | 31,468,863 B |

For the 100-occurrence detail, the shared summary/histogram/four-breakdown query
took 271 ms; the occurrence query took 273 ms. Fallback resolution alone read
516 rows / 29,888 bytes in 11 ms. The test verified the deduplicated
counts, histogram sums, breakdown sums and percentages. Increasing the page from
1 to 100 did not add SQL queries. At 100,000 distinct logs, list/100 took 71 ms,
list/100 with series 139 ms, and detail/100 239 ms in a preceding run. An earlier
million-log run measured 343 / 643 / 503 ms respectively, illustrating normal
run-to-run variability rather than an established latency percentile.

Both public methods use the shared 5-second cache and singleflight miss
coalescing. The load test repeated each exact request immediately and asserted
zero additional ClickHouse queries: cached lists took 50–8,032 microseconds and
cached details 149–1,644 microseconds, including result comparisons in the test.
Cache keys include the application, period, filters, series flag and pagination;
HTTP/MCP authorization and Enterprise checks must still precede every read.
Rolling windows with different absolute bounds produce different cache keys.

## Query shape and limits

- List: one aggregate query, plus one batched series query when requested.
- Detail: one `GROUPING SETS` query for summary, histogram and all breakdowns,
  plus one occurrence query. An old fallback identifier can add one targeted
  `error_groups` resolution query. Existing update labels use one bounded
  PostgreSQL lookup, independently of the number of displayed updates.
- The common source applies application, event-time and Observe filters before
  deduplication by `content_key`. Latest group metadata uses `argMax` on the
  qualified update/fingerprint keys. Aggregate queries do not read stored
  symbolicated traces and do not use `FINAL`.
- Series and detail push candidate raw keys into the log filter before
  deduplication. Older mapping versions can admit candidates, but the final
  latest-version join determines actual membership. Occurrence payloads are
  fetched only for the selected content keys.
- ClickHouse CTEs are expanded, not materialized. An aggregate query contains
  two filtered log reads: the event source and relevant group keys. The
  occurrence query adds a third read to fetch bounded payloads. Constant query
  count therefore does not imply a single table scan.
- `EXPLAIN` showed event-time partition pruning and the application primary-key
  predicate. For a scoped group, the plan also used `update_id IN` with sixteen
  candidates. In this run the log primary key retained 136 of 287 relevant
  granules; group metadata retained 7 of 19 granules. Part counts vary as merges
  and unrelated test fixtures change.

The existing `observe_logs` key is `(app_id, update_id, timestamp)`, partitioned
monthly, with a device bloom index. It has no fingerprint or content-key index.
Every selected group occurs in every update in this fixture, so group scoping
reduces deduplication memory considerably but still reads most application log
granules. The 31-day bound limits time, not event volume: read work remains
linear in the selected application's logs. Narrow update/date/device filters
can help; production capacity must be checked against actual volume, payload
size, group cardinality and concurrency before claiming a latency target.

The view covers errors and fatal occurrences in `observe_logs`, including the
manual `xprem_js_crash` event. Native launch-crash reports in
`device_health_events` require a separate identity adapter and are outside V1.
