# ⚡️ Barghvim

**Barghvim** = _Bargh_ (برق / Power) + _Taqvim_ (تقویم / Calendar)

A tiny Go service that fetches planned power outages from the official API (برق من) and exposes them as an **iCalendar (.ics)** feed. Subscribe once, and see all upcoming blackouts directly in your Google, Apple, or Outlook calendar.

---

## 🚀 How it works

- Pulls planned outages for your **bill number (شناسه قبض)** using your API token
- Converts Shamsi (جلالی) dates to Gregorian
- Serves an `.ics` feed ready to import into any calendar app

The upstream API is only reachable from inside Iran, so Barghvim has to run somewhere with Iranian network access — a local machine or a VPS inside the country.

---

## 📅 Result

You'll see events like:

```
⚡️ Planned Power Outage
⏰ 2025-09-07, 09:00 → 11:00
```

directly in your calendar.

---

## 🖼 Screenshots

<br>
<div align="center">
  <p>
    <img src="assets/screenshot.jpeg" alt="screenshot" width="800" />
  </p>
</div>

---

## 🏃 Running it

### With Go

```bash
go run .
```

### With Docker

```bash
cp .env.example .env
docker compose up -d
```

Or without compose:

```bash
make image
docker run --rm -p 8080:8080 barghvim:dev
```

The image is a static binary on `scratch` (~10 MB) with the timezone database compiled in, running as an unprivileged uid.

### Configuration

All configuration is environment variables — see `.env.example`.

| Variable             | Default                    | Description                            |
| -------------------- | -------------------------- | -------------------------------------- |
| `ADDR`               | `:8080`                    | Listen address; wins over `PORT`       |
| `PORT`               | —                          | Port shorthand when `ADDR` is unset    |
| `LOG_LEVEL`          | `info`                     | `debug`, `info`, `warn`, or `error`    |
| `OUTAGE_WINDOW_DAYS` | `7`                        | Days of lookahead, 1–60                |
| `UPSTREAM_URL`       | برق من planned-blackouts   | Override the upstream API, for testing |

---

## 🛠 Usage

Subscribe in Google Calendar, Apple Calendar, Outlook, etc.

```
http://<your-host>:8080/v1/<bill_number>/cal.ics?token=<your_token>
```

Example:

```
http://192.168.1.10:8080/v1/1234567890/cal.ics?token=eyJhbGciOi...
```

- Replace `<bill_number>` with your real شناسه قبض
- Replace `<your_token>` with your برق من API token
- Token is valid ~6 months; renew and update the URL when needed

`GET /healthz` returns `ok` for liveness checks.

---

## 🔒 Privacy

Barghvim is **stateless**.

- No data, bill numbers, or tokens are stored anywhere.
- Everything is fetched live from the برق من API and returned as an `.ics` feed.
- Request logs record the matched route only — never the token or the bill number.
- Your subscription URL is private — anyone with the link can view it, so **don't share it publicly**.

If you expose Barghvim beyond your own network, put it behind TLS. The token travels in the query string, because calendar clients cannot set request headers on a subscription.

---

## 🧑‍💻 Development

```bash
make check   # fmt + vet + test -race
make test    # tests only
make cover   # coverage report in the browser
make help    # list all targets
```

The project is a single flat package — `main.go` wires things up, `server.go` handles HTTP, `outages.go` talks to the upstream API, `jalali.go` converts dates, and `calendar.go` renders the feed.

---

## 🤝 Contributing

Contributions are welcome!

1. Fork the repo
2. Create a new branch (`git checkout -b feature/your-feature`)
3. Commit changes (`git commit -m 'Add new feature'`)
4. Push to your fork (`git push origin feature/your-feature`)
5. Open a Pull Request

Issues, bug reports, and ideas are also appreciated.

---

## 🔗 Links

- Repo: [github.com/mokhajavi75/barghvim](https://github.com/mokhajavi75/barghvim)
