# OTel Observability Stack

Local stack for testing ld-relay's OTLP log and metric export.

## Components

| Service | Port | Purpose |
|---------|------|---------|
| OTel Collector | 4317 (gRPC), 4318 (HTTP) | Receives OTLP telemetry from relay |
| Loki | 3100 | Stores and indexes logs |
| Prometheus | 9090 | Scrapes metrics from the collector |
| Grafana | 3000 | UI for exploring logs and metrics |

## Quick start

```bash
# Start the stack
cd dev/otel
docker compose up -d

# Run relay with OTLP enabled (from the repo root)
USE_OTLP=true go run .

# Open Grafana
open http://localhost:3000
```

## Viewing logs in Grafana

1. Go to http://localhost:3000/explore
2. Select **Loki** as the data source (should be default)
3. Run the query `{service_name="ld-relay"}` to see all relay logs
4. Use the label filters to narrow down:
   - `{service_name="ld-relay"} | json` to parse structured fields
   - `{service_name="ld-relay", severity="ERROR"}` for errors only

## Viewing metrics in Grafana

1. Go to http://localhost:3000/explore
2. Switch the data source to **Prometheus**
3. Browse available metrics — relay's OTLP metrics flow through the
   collector and are scraped by Prometheus
4. Try queries like `rate(http_server_duration_sum[5m])` or use the
   metric browser to explore what's available

## Viewing raw collector output

The collector also uses the `debug` exporter, so all received telemetry
(logs and metrics) is printed to its stdout:

```bash
docker compose logs -f otel-collector
```

## Data flow

```
ld-relay
  │
  │  OTLP (gRPC :4317)
  ▼
OTel Collector
  ├──► debug (stdout)
  ├──► Loki ──► Grafana (logs)
  └──► Prometheus exporter (:8889)
          │
          ▼
       Prometheus ──► Grafana (metrics)
```

## Cleanup

```bash
docker compose down -v
```
