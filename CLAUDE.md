# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Layout

The repo root is a release package (README.md, DEPLOY.md, `bin/`, `docs/`, `SHA256SUMS.txt`). The Go module (`baidupcs-web`) lives in **`source/`** — there is no `go.mod` at the root, so run all `go` commands from `source/`. Not a git repo.

`baidupcs-web` is a single-binary web client for Baidu Netdisk, wrapping the upstream CLI library `github.com/qjfoidnh/BaiduPCS-Go` (pinned v4.0.2). The frontend is plain static files embedded with `go:embed`; there is no Node/npm and no frontend build step.

## Commands (run in `source/`)

```bash
go build ./... && go vet ./... && go test ./...      # full regression (also run with -race for xfer/server changes)
go test ./internal/xfer -run TestName -race          # single test / package
bash scripts/build.sh                                # CGO_ENABLED=0 linux/amd64+arm64 -> source/bin/ (version via -X main.version)
bash scripts/dev.sh                                  # dev mode: go run, 127.0.0.1:8080, /baidu prefix, DEBUG logs
bash scripts/smoke_all.sh                            # end-to-end smoke with stub download source; success prints SMOKE_ALL_OK
bash scripts/smoke_{dl,upload,sharetmp}.sh           # individual smoke scripts
```

Scripts hardcode `GO=/usr/local/go/bin/go` (build.sh honours `$GO`). Run the server: `bin/baidupcs-web-linux-amd64` (defaults: `-listen 127.0.0.1:8080 -base-path /baidu`; env `BPW_LISTEN` / `BPW_BASE_PATH` change the defaults). It is exposed on the tailnet via `tailscale serve --bg --set-path /baidu http://127.0.0.1:8080/baidu` (the mount path is stripped by tailscale, so the target must include `/baidu`); the raw port is loopback-only. If module download fails, `go env -w GOPROXY=https://goproxy.cn,direct`. Don't start long-lived server processes on the user's behalf — service lifecycle belongs to the user.

## Architecture

Read `docs/design/baidupcs-web.md` (architecture, §0 upstream findings, §11 wave table) and `docs/requirements-baidupcs-web.md` (FR-xx / NFR-xx IDs referenced throughout code comments). `docs/dev/2026-10-24-baidupcs-web.md` is the bug/lessons log — check it before touching related areas.

Layering under `source/internal/`:
- `cmd/baidupcs-web/main.go` — flags/env only; all logic is in `internal/*`.
- `server` — Go 1.22+ stdlib `ServeMux` routing + hand-rolled middleware chain: recover → rate limit (upload, 10/min/IP) → leveled, redacted logging. **There is no app-level auth** (password/session login was removed); access control is loopback-only binding + tailscale serve (tailnet only). `mountBasePath` mounts everything under `-base-path`; anything outside the prefix is 404. `NewWithEngine` injects a stub `xengine.Engine` for tests/smoke (via the package-level `engineOverride`, restored after construction).
- `api` — REST handlers (`/api/*`), SSE (`/api/events`), error→HTTP mapping in `errors.go`.
- `xengine` — **the only package allowed to import `github.com/qjfoidnh/*`** (isolates upstream upgrades). Upstream `internal/pcsconfig` is not importable, so multi-account credentials (`accounts.json`), the `BaiduPCS` instance pool and the config whitelist are reimplemented here. Share-transfer is an 8-step orchestration copied 1:1 from upstream `RunShareTransfer`.
- `xfer` — transfer manager: upload queue (`max_upload_load`), download semaphore (`max_download_load`), state machine, SSE event bus (publish snapshots, 500ms throttle). Downloads are a **server-side streaming proxy** (not written to disk; Range pass-through, 3 Range retries on broken streams) because Baidu dlinks require the `NetdiskUA` User-Agent. The dlink resolver is a function field (`ProxyConfig.Resolve`) injected, not a method override.
- `uploader` / `tempmgr` — chunked (8MB) upload receive to a temp dir, rapid-upload (秒传) first, 2GB disk watermark, temp file deleted ≤10s after terminal state, orphan sweep.
- `sharetmp` — FR-40 share-link direct download: temp transfer into the user's drive, stream, delete temp dir after terminal state.
- `session` — only HMAC download-link signing (`SignToken`/`VerifyToken`, key = `config.SessionKey`). `config` / `taskdb` — `config.json`/`accounts.json`/`tasks.json` (JSON files, 0600, mutex-serialized); only terminal task states are persisted.
- `webui` — embeds `dist/` (`index.html`, `app.js`, `style.css`, and a self-written `pv.js` petite-vue replacement); non-`/assets/` paths fall back to `index.html` (hash routing). The handler injects `<base href="{base-path}/">`, so frontend code must use **relative** URLs (`api/...`, `assets/...`, never a leading `/`); signed URLs returned by the server (`/api/dl?...`) get their leading `/` stripped in `app.js`.

## Conventions and gotchas

- Interface-first design: package-level interfaces (`xengine.Engine`, `xfer.Manager`, …) are the contract; implementations live in `*_impl.go`. Don't change interface signatures casually, and don't import upstream outside `xengine`.
- Error semantics: "no Baidu account linked" (engine `ErrAuth`) is `409 BAIDU_NOT_LINKED`; the frontend only shows the message and stays on the page.
- Config whitelist: risk-sensitive upstream fields (`appid`, `pcs_ua`, `pan_ua`, `pcs_addr`, proxy…) are read-only; writes return 403.
- Tests use stub engines/pcs injected into the pool (do not rely on struct-embedding method overrides — inner receivers ignore outer overrides). Stub Baidu responses must include both `errno` and `error_code`. Tests that hit the network must tighten client timeouts (upstream default is 50s).
- IPv6: `xengine.applyV4FirstDial` pre-binds v4 via the upstream hook `requester.SetTCPHostBind` when v6 to Baidu is unreachable (or `BPW_FORCE_V4=1`). Prefer upstream's exported hooks over overriding the client transport.
- Old `docs/` and README/DEPLOY still describe password login and `0.0.0.0:8080`; the code no longer does that. Planned: run under Docker (publish only to `127.0.0.1`, set `BPW_LISTEN=0.0.0.0:8080` inside the container).
- Comments and docs are in Chinese and reference FR/NFR/wave IDs; match that style.
