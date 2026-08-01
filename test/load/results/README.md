# Load test results

Measured capacity of a single xprem instance serving the expo-updates protocol.

| File | Contents |
|---|---|
| `2026-08-01-summary.json` | Configuration, traffic model, per-phase results, limits |
| `2026-08-01-timeseries.csv` | Every metric over time, long format |
| `../loadtest.js` | The k6 script that produced them |
| `../grafana-dashboard.json` | Dashboard used to watch the runs |

The CSV is one point per row, ready for any charting tool:

```
t_seconds,source,metric,scenario,value
```

`t_seconds` is seconds since the run started. `source` is `k6`, `server` or
`database`. `scenario` is set for the k6 latency and throughput series
(`fleet`, `probe`, `push_storm`) and empty otherwise.

## Run of 1 August 2026

**Configuration.** One EC2 `c6g.medium` (1 vCPU, 2 GiB) running the server in
Docker, an RDS PostgreSQL `db.t4g.small` (2 vCPU, 2 GiB), Google Cloud Storage
for update bundles, and a separate `c7g.xlarge` in the same VPC generating load
with k6. Code signing and device telemetry both enabled. No load balancer: the
generator talks plain HTTP to the private address, since TLS is terminated
upstream in a real deployment.

**Method.** Requests are real expo-updates polls from a fixed pool of 100,000
devices with stable identifiers, every response RSA-signed. The load is applied
in an open model: the target rate is imposed whether or not the server keeps
up, so queueing shows as latency instead of being hidden by a slower client.
Three phases: the peak-hour traffic of real fleets, a slow ramp to locate the
saturation point, and a rollout pushed to the entire fleet.

**Results.** 294,372 requests, zero errors, zero dropped iterations.

| Phase | Rate | mean | p95 | p99 |
|---|---|---|---|---|
| fleet | 230 req/s | 1.46 ms | 1.58 ms | 2.35 ms |
| probe | 650 req/s | 1.02 ms | 1.39 ms | 2.28 ms |
| push_storm | 938 req/s | 3.15 ms | 20.5 ms | 55.2 ms |

All three statistics share one window: the phase minus the first 60 seconds of
the run, which are the cold start (empty caches, empty connection pool, first
signature computed). Percentiles are the peak reached in that window, the mean
is its average. The cold start itself peaks at 98 ms and is reported separately
in the summary.

The saturation knee was not reached at the highest rate tested. The server
peaked at 87.8% CPU during the storm; the database peaked at 27% CPU and
accrued CPU credits over the run rather than depleting them.

Read `2026-08-01-summary.json` for the full numbers, and its `limits` array for
what this run does not establish.

## Reproducing

```bash
k6 run -o experimental-prometheus-rw \
  --tag testid=<run-name> \
  -e BASE_URL=http://<server>:3000 \
  -e APP_ID=<app-id> \
  -e IOS_UPDATE_ID=<ios-manifest-id> \
  -e ANDROID_UPDATE_ID=<android-manifest-id> \
  test/load/loadtest.js
```

Every published run reports `dropped_iterations`. A run whose generator could
not inject the requested load is not a measurement of the server.
