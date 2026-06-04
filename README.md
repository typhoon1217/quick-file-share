# quick-file-share

Small intranet file/text share server with expiring links, browser upload UI, txt/md preview, copy buttons, QR codes, and optional access password.

## Run

```sh
go run .
```

Open `http://localhost:8080`.

For an intranet host:

```sh
QFS_ADDR=0.0.0.0:8080 QFS_DATA_DIR=/srv/quick-file-share ./bin/quick-file-share
```

## Build

```sh
go build -o bin/quick-file-share .
./bin/quick-file-share
```

## Configuration

Every setting can be passed as a flag or environment variable.

| Flag | Env | Default |
| --- | --- | --- |
| `-addr` | `QFS_ADDR` | `0.0.0.0:8080` |
| `-data-dir` | `QFS_DATA_DIR` | `./data` |
| `-max-upload-size` | `QFS_MAX_UPLOAD_SIZE` | `5GB` |
| `-default-ttl` | `QFS_DEFAULT_TTL` | `24h` |
| `-cleanup-interval` | `QFS_CLEANUP_INTERVAL` | `10m` |
| `-preview-size` | `QFS_PREVIEW_SIZE` | `4MB` |
| `-access-password` | `QFS_ACCESS_PASSWORD` | disabled |
| `-public-base-url` | `QFS_PUBLIC_BASE_URL` | request host |
| `-base-path` | `QFS_BASE_PATH` | disabled |

Examples:

```sh
QFS_ACCESS_PASSWORD='change-me' ./bin/quick-file-share
QFS_MAX_UPLOAD_SIZE=1GB QFS_DATA_DIR=/srv/quick-file-share ./bin/quick-file-share
QFS_PUBLIC_BASE_URL=https://fileshare.intra ./bin/quick-file-share
QFS_PUBLIC_BASE_URL=https://fileshare.intra QFS_BASE_PATH=/qfs ./bin/quick-file-share
```

## Behavior

- Files and text are stored under `data/items`.
- The default expiry is 24 hours.
- Expired items are deleted by a background cleanup loop and also lazily deleted when accessed.
- Upload responses include a delete URL. Anyone with that delete token can delete the item before expiry.
- When `QFS_ACCESS_PASSWORD` is set, the upload UI and share pages require the password.

## Security checklist

- Keep runtime data and deployment scratch files outside git.
- Use `QFS_ACCESS_PASSWORD` when the service is reachable beyond a trusted LAN.
- Set `QFS_PUBLIC_BASE_URL` to the HTTPS URL exposed by the reverse proxy.
- Set `QFS_BASE_PATH` when serving under a path prefix such as `/qfs`.
- Run with TLS at the proxy when password protection is enabled.

## API

Upload a file:

```sh
curl -F file=@./example.txt -F ttl=24h http://localhost:8080/api/upload
```

Share text:

```sh
curl -H 'Content-Type: application/json' \
  -d '{"name":"note.md","text":"# hello","ttl":"1h"}' \
  http://localhost:8080/api/text
```

TTL values support Go durations such as `10m`, `1h`, `24h`, plus day values such as `3d` and `7d`.
