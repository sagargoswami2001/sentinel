# Sentinel 🛰️

> Self-hosted uptime monitoring that fits in a single binary.

<!-- Add a screenshot here once you deploy — it's the most viewed line in any README -->
<!-- ![Sentinel dashboard](docs/screenshot.png) -->

I got tired of paying for status-page SaaS tools that do one thing: show a green dot. Sentinel checks your sites, APIs, databases, and raw TCP ports — all from a dark dashboard you own and run anywhere. No account, no subscription, no data leaving your network.

---

## What it does

Paste any URL into the bar at the top. Hit **Check once** for a quick one-off result, or **+ Monitor** to add it to the board permanently. Hover a card and click **✕** to stop watching it. That's the whole workflow.

Behind the scenes, every target gets checked concurrently on a configurable interval. Each card shows live latency history as a sparkline, a row of green/red tick bars for the last ~30 minutes, current uptime %, and TLS certificate days remaining. When something flips from up to down (or recovers), the incident lands in the event feed and fires a webhook alert — simultaneously, to as many channels as you've configured.

**Supported check types:**

| URL format | How it's checked |
|---|---|
| `https://mysite.com` | HTTP GET — status code, latency, TLS cert expiry |
| `192.168.1.10:3000` or `localhost:5000` | Same, with `http://` assumed |
| `tcp://host:22` | Raw TCP dial — SSH, Redis, anything that listens on a port |
| `mysql://user:pass@host:3306/db` | Real MySQL connection + `db.Ping()` |
| `postgres://user:pass@host/db` | Real Postgres connection + `db.Ping()` |

**Alert channels** (configured from the 🔔 panel in the dashboard — no YAML needed):
Slack · Microsoft Teams · Google Chat · Discord · any custom webhook

---

## Get started

Requires [Go 1.22+](https://go.dev/dl/). No database, no Docker, no other services.

```sh
git clone https://github.com/sagargoswami2001/sentinel.git
cd sentinel
go run ./cmd/sentinel serve
```

Open **http://localhost:8080**.

To build a binary you can copy to a server:

```sh
# Linux (from any machine)
GOOS=linux GOARCH=amd64 go build -o sentinel ./cmd/sentinel

# Windows
go build -o sentinel.exe ./cmd/sentinel
```

---

## Running it

```sh
# Dashboard on :8080, checks every 30s
sentinel serve

# Tune the interval and port
sentinel serve --interval 10s --listen :9090

# One-shot check — prints a table, exits 1 if anything is down
# (useful in cron jobs or CI)
sentinel check
```

Press **`/`** on the dashboard to jump focus to the URL bar.

---

## Configuration

Targets added via the browser are saved back to the config file automatically, so they survive restarts. You can also manage `configs/sentinel.yaml` by hand:

```yaml
defaults:
  timeout_seconds: 10
  expect_status: 200

targets:
  - name: Production API
    url: https://api.mysite.com/health

  - name: Staging VM
    url: http://10.0.0.5:8000
    timeout_seconds: 3

  - name: Auth service (returns 401 when healthy)
    url: https://auth.internal.mysite.com
    expect_status: 401

  - name: SSH — build server
    url: tcp://build-host:22

  - name: Azure MySQL
    url: mysql://sentinel:password@mydb.mysql.database.azure.com:3306/app
```

### Alerts

Open the **🔔 Notifications** panel in the dashboard, pick a platform, paste the webhook URL, click **Add**. Send a test alert to confirm it works before you need it. Alerts fire on every up→down and down→up transition for every target on the board.

To keep webhook URLs out of committed YAML (recommended for public repos), use env vars instead:

```sh
SENTINEL_SLACK_WEBHOOK=https://hooks.slack.com/... sentinel serve
```

Where to get a webhook:
- **Slack** — api.slack.com/apps → Incoming Webhooks
- **Teams** — channel → ⋯ → Workflows → "Post to a channel when a webhook request is received"
- **Google Chat** — space settings → Apps & integrations → Webhooks
- **Discord** — channel → Edit → Integrations → Webhooks

### Prometheus metrics

Sentinel exposes a `/metrics` endpoint on the same port:

```sh
curl http://localhost:8080/metrics
```

```
sentinel_target_up{name="Production API",url="..."}           1
sentinel_target_latency_seconds{name="Production API",url="..."} 0.043
sentinel_target_cert_days_left{name="Production API",url="..."}  56
sentinel_checks_total{name="Production API",url="...",status="up"} 24
```

Add to your `prometheus.yml`:

```yaml
scrape_configs:
  - job_name: sentinel
    static_configs:
      - targets: ['localhost:8080']
```

### JSON API

```sh
curl http://localhost:8080/api/status
```

```json
{
  "last_run": "2026-08-06T14:30:00Z",
  "statuses": [
    {
      "name": "Production API",
      "url": "https://api.mysite.com/health",
      "up": true,
      "status_code": 200,
      "latency_ms": 43,
      "cert_days_left": 56
    }
  ]
}
```

---

## Deploy

### Render (free)

1. Push this repo to GitHub
2. [render.com](https://render.com) → sign in with GitHub → **New → Web Service** → select the repo
3. Render detects the Dockerfile automatically → pick **Free** → **Deploy**

Your dashboard is live at `https://<app-name>.onrender.com` in about two minutes. The free tier sleeps after 15 idle minutes — first visit takes ~30s to wake. For a permanent instance that's always on, Docker on any cheap VPS works well.

### Docker

```sh
docker build -t sentinel .
docker run -d --name sentinel --restart unless-stopped \
  -p 8080:8080 \
  -v $(pwd)/configs/sentinel.yaml:/etc/sentinel/sentinel.yaml \
  sentinel
```

Or pull the image published automatically on every push to main:

```sh
docker run -d -p 8080:8080 \
  -v $(pwd)/configs/sentinel.yaml:/etc/sentinel/sentinel.yaml \
  ghcr.io/sagargoswami2001/sentinel:latest
```

The container reads the `PORT` environment variable, so it drops straight into Render, Railway, Fly.io, or any other PaaS without config changes.

---

## Project layout

```
sentinel/
├── cmd/sentinel/        # thin CLI entrypoint — flag parsing, nothing else
├── internal/
│   ├── checker/         # HTTP, TCP, MySQL, Postgres health checks
│   ├── config/          # YAML load + save, default merging
│   ├── metrics/         # Prometheus gauge and counter definitions
│   ├── notify/          # Slack, Teams, GChat, Discord, webhook senders
│   ├── report/          # colored terminal table for sentinel check
│   └── server/          # HTTP server, templates, CSS — all go:embed'd
└── configs/sentinel.yaml
```

---

## License

[MIT](LICENSE) — do whatever you want with it.
