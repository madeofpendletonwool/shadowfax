# Shadowfax

Ephemeral file transfer with a PIN. Drop a file, get a PIN, share it. Files are
encrypted at rest, and delete themselves when they expire. No accounts, no
sign-up, one binary.

Built for humans *and* robots — point an AI agent at the root URL and it gets
API docs instead of a JavaScript bundle, so it can upload a file and hand the
PIN back to you without any glue code.

```
curl -T ./build.zip https://your-host/api/upload/build.zip
# -> {"pin":"482107","share_url":"https://your-host/?pin=482107", ...}
```

---

## Contents

- [How it works](#how-it-works)
- [Encryption](#encryption)
- [Running it](#running-it)
- [Configuration](#configuration)
- [API](#api)
- [What happens to your files](#what-happens-to-your-files)
- [Development](#development)

---

## How it works

1. **Send** — drag files into the browser (or `curl` them up). Optionally add a
   text note and pick an expiry between 1 hour and 7 days.
2. **Encrypt** — every byte is encrypted with AES-256-GCM as it streams to disk.
   Nothing is ever written in the clear.
3. **Get a PIN** — the server returns a unique 6-digit PIN and a share link.
4. **Receive** — the recipient enters the PIN at the site root, or opens
   `https://your-host/?pin=482107`, and downloads the files individually or as a
   zip, decrypted on the way out.
5. **Expire** — a background sweep runs every minute, deleting expired transfers
   and destroying their keys.

The whole app ships as a single Go binary: the React frontend is compiled by
Vite and embedded into the executable with `go:embed`, and state lives in a
SQLite file. There is no external database, cache, or object store.

**Stack:** Go 1.23 + chi + modernc.org/sqlite (pure-Go, no cgo) · React 18 +
TypeScript + Tailwind + Vite · Alpine container, multi-arch amd64/arm64.

---

## Encryption

Uploads are encrypted at rest with **AES-256-GCM**, always — there is no plaintext
mode and nothing to turn on. Keys are layered:

```
  master key (KEK)          ENCRYPTION_KEY, else generated into $DATA_DIR/.encryption-key
        │ wraps
  data key (DEK)            fresh 32 random bytes per file, stored only wrapped, in the DB
        │ encrypts
  file body                 chunked AES-256-GCM, 64 KiB at a time
```

**Per-file keys.** Every file (and every text note) gets its own data key. That
key is never stored in the clear — only its wrapped form, in the database row.

**Streaming.** Bodies are encrypted in 64 KiB authenticated chunks, so a 40 GB
file encrypts and decrypts incrementally and is never held in memory. Each
chunk's nonce carries the chunk counter and an end-of-stream flag, so truncating
or reordering the file is detected rather than silently returning short data.

**Crypto-shredding.** Because the only copy of a file's key lives in its database
row, deleting that row destroys the key. An expired transfer is unrecoverable
even if its blocks survive on the underlying disk, which is the guarantee a plain
`unlink` could never give.

### Key management

If `ENCRYPTION_KEY` is set it is used as the master key. Otherwise a key is
generated on first boot and written to `$DATA_DIR/.encryption-key` (mode 0600),
and the server logs a warning — because a key sitting beside the data it protects
means a copy of the volume is enough to decrypt everything.

```bash
openssl rand -base64 32      # then set it as ENCRYPTION_KEY
```

Either base64 or hex is accepted, as long as it decodes to 32 bytes.

> **Back the key up.** Lose the master key and every stored transfer is
> permanently unreadable. That is the point of the design, and it applies to you
> as much as to an attacker.

### What is *not* encrypted

Filenames, file sizes, and expiry times are stored as plaintext metadata in the
database — only file contents and text notes are encrypted. Transport encryption
is still your reverse proxy's job (see below); this protects data at rest, not in
flight.

Transfers created before this version stay readable as plaintext until they
expire; the server logs a count at startup. Every new upload is encrypted.

---

## Running it

### Docker Compose (recommended)

```yaml
services:
  shadowfax:
    image: ghcr.io/madeofpendletonwool/shadowfax:latest
    ports:
      - "8085:8080"
    volumes:
      - shadowfax_data:/data
    environment:
      - PORT=8080
      - DATA_DIR=/data
      - ENCRYPTION_KEY=<openssl rand -base64 32>
      - API_KEY=change-me
      - TRUSTED_PROXY=true      # only if a reverse proxy fronts this
    restart: unless-stopped

volumes:
  shadowfax_data:
```

```bash
docker compose up -d
```

Then open http://localhost:8085. The shipped `docker-compose.yml` has every
option listed and commented.

Both `ENCRYPTION_KEY` and `API_KEY` can be omitted for a quick local trial — a
key is generated into the volume and uploads stay open — but set them on
anything reachable from outside your machine.

### Docker

```bash
docker run -d -p 8085:8080 -v shadowfax_data:/data \
  ghcr.io/madeofpendletonwool/shadowfax:latest
```

### From source

```bash
cd frontend && npm install && npm run build   # emits into backend/static/
cd ../backend && go build -o shadowfax .
./shadowfax
```

The Go build needs `backend/static/` to exist, because that directory is
embedded into the binary — build the frontend first.

### Behind a reverse proxy

Shadowfax serves plain HTTP and does not terminate TLS. Run it behind Caddy,
nginx, or Traefik, and **set `TRUSTED_PROXY=true`** so it honours
`X-Forwarded-Proto`/`-Host` when building share URLs and `X-Forwarded-For` when
rate limiting. Without it, share links come out as `http://` and every client
behind the proxy shares a single rate-limit bucket.

```caddyfile
files.example.com {
    reverse_proxy localhost:8085
}
```

Caddy sets those headers for you. Two things to check when proxying large
uploads: raise the request body limit (nginx's `client_max_body_size` defaults to
1 MB) and the read/send timeouts, or long transfers will be cut off mid-stream.

---

## Configuration

All configuration is environment variables. Every one has a working default;
the two worth setting on any public instance are `ENCRYPTION_KEY` and `API_KEY`.

### Core

| Variable | Default | Description |
|----------|---------|-------------|
| `PORT` | `8080` | Port to listen on. |
| `DATA_DIR` | `./data` | SQLite and encrypted uploads. Files live in `$DATA_DIR/files/<pin>/`, the database in `$DATA_DIR/shadowfax.db`. |
| `ENCRYPTION_KEY` | *(generated)* | Master key, base64 or hex, decoding to 32 bytes. Generated into `$DATA_DIR/.encryption-key` when unset. |
| `API_KEY` | *(unset)* | If set, uploads and deletes require it. **Downloads stay open regardless** — anyone with a PIN can fetch. |
| `UPLOAD_PASSWORD` | *(unset)* | Password for signing in on the web UI to upload. Falls back to `API_KEY` when unset; setting either gates uploads. The browser gets a 30-day signed, HttpOnly, SameSite=Strict cookie, and changing the password signs everyone out. |
| `RATE_LIMIT_LOGIN_FAILURES` | `5` | Wrong passwords before a login lockout (`RATE_LIMIT_LOCKOUT_MINUTES`). |
| `PIN_LENGTH` | `6` | Digits per PIN, 4–10. Each extra digit multiplies guessing work by 10. |

### Deployment

| Variable | Default | Description |
|----------|---------|-------------|
| `TRUSTED_PROXY` | `false` | Trust `X-Forwarded-For`/`-Proto`/`-Host`. Set it **only** behind a proxy you control — see the warning below. |
| `ALLOWED_ORIGINS` | *(unset)* | Comma-separated origins allowed to call the API from a browser. Unset means same-origin only. `*` allows any. |

### Limits

| Variable | Default | Description |
|----------|---------|-------------|
| `MAX_UPLOAD_BYTES` | `5368709120` (5 GiB) | Per-request upload ceiling. `0` disables it. |
| `MAX_EXPIRY_SECONDS` | `604800` (7 days) | Upper bound on `?expiry=`; larger values are clamped. |
| `RATE_LIMIT_LOOKUPS_PER_MIN` | `10` | PIN lookups per IP per minute. |
| `RATE_LIMIT_DOWNLOADS_PER_MIN` | `60` | Downloads per IP per minute. |
| `RATE_LIMIT_UPLOADS_PER_MIN` | `20` | Uploads per IP per minute. |
| `RATE_LIMIT_FAILURES` | `20` | Consecutive wrong PINs before that IP is locked out. A success resets the count. |
| `RATE_LIMIT_LOCKOUT_MINUTES` | `15` | How long a lockout lasts. |

Send the API key as either header:

```
X-API-Key: <your key>
Authorization: Bearer <your key>
```

> **`TRUSTED_PROXY` cuts both ways.** Leave it off and every request behind a
> proxy appears to come from the proxy's address, so all clients share one rate
> limit bucket. Turn it on while directly exposed and anyone can forge
> `X-Forwarded-For` to sidestep the limiter entirely — and forge
> `X-Forwarded-Host` to make your server hand out share links pointing at their
> domain. Match the setting to how the server is actually reachable.
>
> With it on, the rate limiter keys on the **rightmost** `X-Forwarded-For` entry,
> the address your proxy saw, so a client can't dodge it by sending its own
> header. If several proxies are chained, the rightmost one must be the hop
> that talks to clients.

---

## API

Every upload response is self-describing — it contains the PIN, the share URL,
per-file download URLs, and a plain-English `message` — so an agent can relay
the result without constructing any URLs itself.

Live docs with your real hostname baked in are at `GET /llms.txt`, and the root
URL serves the same docs to any client whose User-Agent doesn't contain
"Mozilla". `GET /api` with `Accept: application/json` returns a structured
endpoint listing.

### Upload — raw body (simplest)

```bash
curl -T ./build.zip https://your-host/api/upload/build.zip
```

`PUT` or `POST` to `/api/upload/{filename}` with the file as the raw request
body. One file per transfer.

### Upload — multipart (multiple files + a note)

```bash
curl -F "files=@./a.zip" -F "files=@./b.pdf" -F "text=here you go" \
  "https://your-host/api/transfer?expiry=86400"
```

### Expiry

Append `?expiry=<seconds>` to either upload endpoint. Default `3600` (1 hour),
clamped to `MAX_EXPIRY_SECONDS` (7 days by default). The UI offers 3600, 21600,
86400, 259200, and 604800.

### Response (HTTP 201)

```json
{
  "pin": "482107",
  "expires_at": "2026-09-10T22:45:00Z",
  "share_url": "https://your-host/?pin=482107",
  "files": [
    {
      "name": "build.zip",
      "size": 1048576,
      "download_url": "https://your-host/api/transfer/482107/download/build.zip"
    }
  ],
  "zip_url": "https://your-host/api/transfer/482107/zip",
  "encrypted": true,
  "message": "Share PIN 482107 or the link below ..."
}
```

`size` is always the plaintext size, so `Content-Length` on a download matches
what you receive.

### All endpoints

| Method | Path | Auth | Description |
|--------|------|------|-------------|
| `PUT`/`POST` | `/api/upload/{filename}` | API key | Upload one file as the raw body. |
| `POST` | `/api/transfer` | API key | Multipart upload: `files` (repeatable) and `text`. |
| `GET` | `/api/transfer/{pin}` | open | Transfer metadata and file list. |
| `GET` | `/api/transfer/{pin}/download/{filename}` | open | Download a single file. |
| `GET` | `/api/transfer/{pin}/zip` | open | Download every file as a zip, streamed. |
| `DELETE` | `/api/transfer/{pin}` | API key | Delete a transfer immediately. |
| `GET` | `/llms.txt` | open | Plain-text API docs. |
| `GET` | `/api` | open | JSON endpoint listing (with `Accept: application/json`). |
| `GET` | `/api/config` | open | PIN length and limits, so the UI matches the server. |
| `GET` | `/healthz` | open | Liveness probe. |

Expired or unknown PINs return `404`. Read endpoints are rate limited per IP and
return `429` with a `Retry-After` header once you exceed the limit; repeated
wrong PINs trigger a temporary lockout.

---

## What happens to your files

Short version: **file contents are encrypted at rest and shredded on expiry, but
anyone holding a PIN can download, and the app does not do TLS itself.** Read
this before sending anything sensitive.

### Storage

- Uploads are encrypted with AES-256-GCM as they stream to disk, under a
  per-file key. See [Encryption](#encryption) for the full scheme. Files are
  written 0600 in 0700 directories.
- Text notes are encrypted the same way.
- **Filenames, sizes, and expiry times are not encrypted** — they are ordinary
  metadata columns in SQLite. A name like `q3-layoffs-final.xlsx` leaks what a
  transfer is even though its contents do not.
- **There is still no encryption in transit** unless you put a TLS-terminating
  proxy in front. Over plain HTTP, uploads and downloads cross the network in the
  clear regardless of how they are stored.
- Anyone with root on the host can read the master key and decrypt everything.
  Encryption at rest defends against a stolen disk, a copied volume, or a leaked
  backup — not against someone already inside the box.

### Access control

- PINs are 6 digits by default (1,000,000 values), drawn from `crypto/rand` with
  rejection sampling, so they are uniform and unpredictable. Raise `PIN_LENGTH`
  up to 10 if you want more.
- Download endpoints are intentionally unauthenticated so a recipient needs
  nothing but the PIN. **Treat a live PIN as a credential**: whoever has it has
  the files.
- Guessing is rate limited per IP (10 lookups/min by default), and 20 consecutive
  wrong PINs locks that address out for 15 minutes. A correct PIN resets the
  counter, so a recipient who mistypes once is unaffected.
- `API_KEY` and `UPLOAD_PASSWORD` protect uploads and deletes only, compared in
  constant time. They do not protect downloads. Cookie-authenticated writes must
  be same-origin, on top of the cookie being SameSite=Strict.
- CORS is same-origin by default; set `ALLOWED_ORIGINS` to opt specific sites in.
- Uploads are capped at `MAX_UPLOAD_BYTES` (5 GiB) and rate limited, so an open
  instance cannot be trivially used to fill your disk.
- PINs are redacted from the access log, so reading the logs does not hand
  someone every live transfer.

### Deletion

- A sweep runs every 60 seconds, removing expired transfers and their database
  rows — which destroys the only copy of their encryption keys.
- `DELETE /api/transfer/{pin}` does the same immediately.
- `PRAGMA secure_delete` zeroes freed database pages, so a deleted wrapped key
  does not linger in the database file.
- The file bytes themselves are unlinked, not overwritten — but without the key
  they are just noise.

### If you need more

Encryption at rest does not make Shadowfax end-to-end encrypted: the server sees
your plaintext as it streams through, and can decrypt anything it still holds.
For contents the server itself should never be able to read, encrypt before
uploading and share the passphrase over a different channel:

```bash
gpg -c secrets.tar.gz                                   # -> secrets.tar.gz.gpg
curl -T secrets.tar.gz.gpg https://your-host/api/upload/secrets.tar.gz.gpg
```

On any public instance: run behind TLS, set `ENCRYPTION_KEY` and `API_KEY`, set
`TRUSTED_PROXY=true`, and keep expiry times short.

---

## Development

```bash
# Terminal 1 — backend on :8080
cd backend && go run .

# Terminal 2 — frontend on :5173, proxying /api to :8080
cd frontend && npm run dev
```

Production builds run through the multi-stage `Dockerfile`: Node builds the
frontend into `backend/static/`, Go embeds it and compiles a static binary, and
the result is copied into an Alpine image. Pushes to `main` build and publish
multi-arch images to `ghcr.io/madeofpendletonwool/shadowfax` via GitHub Actions.

### Tests

```bash
cd backend && go test -race ./...
```

Covers the encryption round trip (including tamper and truncation detection),
PIN randomness and uniqueness under concurrency, the enumeration lockout, zip
entry safety, and download of filenames containing every character class that
used to break.

### Layout

```
backend/main.go          routes, storage, cleanup, docs
backend/crypto.go        key management and the chunked AES-256-GCM streams
backend/ratelimit.go     per-IP token buckets and the enumeration lockout
frontend/src/config.ts   pulls PIN length and limits from /api/config
frontend/src/App.tsx     send/receive view switching
frontend/src/components/
  DropZone.tsx           drag-drop upload, expiry picker, progress
  PinDisplay.tsx         shows the PIN + share link after upload
  PinEntry.tsx           PIN input for recipients
  FileList.tsx           download individual files or the zip
```
