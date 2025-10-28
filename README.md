# LocalCloud

Personal self-hosted photo/video storage & gallery (LocalCloud)

---

## Overview

LocalCloud is a lightweight personal cloud that runs on your laptop or Raspberry Pi.  
Features:
- Store photos/videos locally (on a mounted SSD partition or system drive)
- Mobile-first responsive UI (Grid/List) with thumbnails and preview
- Device sync API (`/api/sync/upload`) with SHA256 deduplication
- Background backup worker to copy files to a dedicated backup directory
- Search API with optional regex mode (`/api/search?query=...&regex=1`)
- BasicAuth protection and optional HTTPS via tunneling (ngrok or Cloudflare Tunnel)

---

## Quick start (macOS / Linux)

### Prerequisites
- Go 1.20+ (for building the server)
- git
- `make`
- Optional: `ngrok` for secure public HTTPS tunnels

### Build & run locally
1. Clone repo:
```bash
git clone git@github.com:mayur-tolexo/localcloud.git
cd localcloud
```

2. Prepare data directory (example uses `~/localcloud-data`):
```bash
export DATA_DIR=~/localcloud-data
mkdir -p "$DATA_DIR"
```

3. Set credentials and port:
```bash
export APP_USER=mayur
export APP_PASS=strongpassword
export PORT=8080
```

4. Build & run (macOS / Linux native):
```bash
make run-local DATA_DIR=$DATA_DIR PORT=$PORT
```
This runs the server and binds to `http://localhost:8080`. The UI is served at `http://localhost:8080/ui/drive.html`.

---

## Ngrok — quick secure public access (recommended for testing)

Ngrok provides an HTTPS tunnel to your local server. This is ideal for testing from mobile or remote networks without exposing ports.

### Install ngrok (macOS with Homebrew)
```bash
brew install ngrok/ngrok/ngrok
```
or download from https://ngrok.com.

### Sign up & configure
1. Create a free account at https://dashboard.ngrok.com
2. Copy your authtoken from the dashboard.
3. Authenticate your client:
```bash
ngrok config add-authtoken YOUR_NGROK_AUTHTOKEN
```

### Run ngrok (after your server is running)
```bash
ngrok http 8080
```
Ngrok will print a forwarding URL like:
```
Forwarding                    https://abcd-1234.ngrok.io -> http://localhost:8080
```
Open `https://abcd-1234.ngrok.io/ui/drive.html` and log in with `APP_USER` / `APP_PASS`.

**Notes**
- Free ngrok URLs are ephemeral (new URL per session). Use ngrok paid for reserved subdomain.
- Ngrok terminates TLS — traffic between ngrok and your laptop is HTTP. The app uses BasicAuth so credentials are protected in transit (HTTPS to ngrok) but consider additional security for production.

---

## Cloudflare Tunnel (persistent, free)
If you want a stable hostname and automatic TLS without opening ports, use Cloudflare Tunnel (`cloudflared`).

1. Install cloudflared:
```bash
brew install cloudflared
```

2. Login & create tunnel:
```bash
cloudflared tunnel login
cloudflared tunnel create localcloud
cloudflared tunnel route dns localcloud <your-host.example.com>
cloudflared tunnel run localcloud --url http://localhost:8080
```

This maps your chosen hostname to the local server. TLS is handled by Cloudflare.

---

## Mounting external SSD partition (example)

If you want LocalCloud to use a partition on an external SSD:

1. Identify partition (macOS example using diskutil):
```bash
diskutil list
```
2. Create a mount point and mount (Linux example):
```bash
sudo mkdir -p /mnt/localcloud-data
sudo mount /dev/sdX1 /mnt/localcloud-data
sudo chown $(whoami):$(whoami) /mnt/localcloud-data
```

Use that path as `DATA_DIR` when starting LocalCloud:
```bash
export DATA_DIR=/mnt/localcloud-data
make run-local DATA_DIR=$DATA_DIR PORT=8080
```

> You asked to use *one partition only* — ensure the mount path points to the one partition you want LocalCloud to use.

---

## Docker / containerd (optional)

A `docker-compose.yml` and `Makefile` target are included (if available). To run with Docker:
```bash
docker-compose up -d
```
If you prefer `containerd`, run the image/container via your standard tooling. Make sure to mount `$DATA_DIR` into the container and set the required environment variables (`APP_USER`, `APP_PASS`, `PORT`).

---

## Important Environment Variables

- `DATA_DIR` — directory where LocalCloud stores files & DB (required)
- `APP_USER` — BasicAuth username (required)
- `APP_PASS` — BasicAuth password (required)
- `PORT` — port to bind (default `8080`)
- `BACKUP_DIR` — (optional) if you want backups on a different mount

Example:
```bash
export DATA_DIR=~/localcloud-data
export BACKUP_DIR=/mnt/backups
export APP_USER=mayur
export APP_PASS=strongpass
export PORT=8080
```

---

## API endpoints (use with BasicAuth)

- `POST /api/sync/upload` — multipart form upload (`file`, `device_id`)
- `GET /api/sync/status?device_id=...` — recent uploads and backup state
- `GET /api/search?query=...&regex=1` — search (regex optional)
- `GET /api/grid?path=/some/path` — list folder contents (used by UI)
- `GET /api/file?path=...` — stream file
- `GET /api/thumbnail?path=...&w=...` — thumbnail
- `GET /api/metadata?path=...` — metadata for file

All requests must include BasicAuth credentials (unless you change middleware).

---

## Security & production notes

- The README covers **testing** and **personal use**. For production usage:
  - Use strong credentials and rotate them.
  - Prefer Cloudflare Tunnel or a reverse proxy with TLS termination.
  - Consider implementing per-device API tokens instead of BasicAuth.
  - Monitor disk usage and set quotas if needed.
  - Backups should be on a physically separate drive if possible.

---

## Troubleshooting

- `no such column: exif_datetime` — run the migration code included in `internal/api/sync.go` (`InitSyncDB()` handles schema migrations automatically).
- Thumbnails not showing — ensure thumbnail worker is running (`StartThumbnailWorker`) and that `DATA_DIR` has proper permissions.
- CORS preflight errors — ensure CORS middleware is registered before BasicAuth in `main.go`.

---

## Example: Run locally + ngrok (one-liners)

```bash
# start server
DATA_DIR=~/localcloud-data APP_USER=mayur APP_PASS=strongpass PORT=8080 make run-local DATA_DIR=~/localcloud-data PORT=8080 &

# in another terminal, start ngrok
ngrok http 8080
```

Open the ngrok HTTPS URL and log in.

---