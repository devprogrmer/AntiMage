# Authenticated Prometheus metrics

The panel exposes `GET /metrics` only when `ANTIMAGE_PROMETHEUS_TOKEN` is set in its service environment. The endpoint is hidden with HTTP 404 when the token is absent. It accepts the token only through `Authorization: Bearer`; URL query tokens are ignored. Use a dedicated random token, separate from administrator API keys, and scrape the panel over HTTPS.

Example Prometheus configuration:

```yaml
scrape_configs:
  - job_name: antimage-panel
    scheme: https
    metrics_path: /metrics
    static_configs:
      - targets: ['panel.example.com:8000']
    authorization:
      type: Bearer
      credentials_file: /etc/prometheus/secrets/antimage-metrics-token
```

Current metrics are `antimage_up`, `antimage_database_up`, `antimage_go_goroutines`, and `antimage_go_heap_alloc_bytes`. A scrape pings the panel database with a one-second timeout. It does not poll nodes, query Xray statistics, or change accounting state. Node accounting health remains available in the Nodes page and its separate Telegram alert.
