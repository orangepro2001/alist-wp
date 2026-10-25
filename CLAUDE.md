# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Layout

The repo root is the git repo (remote `git@github.com:orangepro2001/alist-wp.git`, branch `main`). The Go module (`baidupcs-web`) lives in **`source/`** — there is no `go.mod` at the root, so run all `go` commands from `source/`. `docs/` holds early design notes from when the project still had its own web UI (historical; password login, SPA, `/api/*` file endpoints no longer exist).

`baidupcs-web` exposes Baidu Netdisk as **WebDAV** (`/dav/`) for OpenList/Alist to mount; OpenList is the only user-facing front end (tailscale `/files`). It wraps the upstream CLI library `github.com/qjfoidnh/BaiduPCS-Go` (pinned v4.0.2). No web pages, no Node/npm.

## Commands (run in `source/`)

```bash
go build ./... && go vet ./... && go test -race ./...   # full regression
go test ./internal/davfs -run TestPut -race             # single test / package
bash scripts/build.sh                                   # CGO_ENABLED=0 linux/amd64+arm64 -> source/bin/ (version via -X main.version)
bash scripts/smoke_all.sh                               # end-to-end with stub engine + stub download source; prints SMOKE_ALL_OK
bash scripts/dev.sh                                     # dev mode: go run, 127.0.0.1:8080, DEBUG logs
go run ./cmd/baidupcs-web account list|add|switch|rm    # account management (HTTP client of the running server)
```

Scripts hardcode `GO=/usr/local/go/bin/go` in smoke_all.sh (build.sh honours `$GO`). If module download fails, `go env -w GOPROXY=https://goproxy.cn,direct`. Don't start long-lived server processes on the user's behalf unless asked — service lifecycle belongs to the user.

## Exposure rule (important)

All services are reachable **only** through the tailscale https domain, via `tailscale serve`; never open extra paths/ports to the network. This server listens on `127.0.0.1:8080` only (plumbing for OpenList) and has **no app-level auth**. Only add `tailscale serve` paths the user asked for; don't touch existing ones (`/`, `/dsh`, `/claude`, `/report`, `:8443`). Account management is CLI-only for this reason.

## Deployment on this machine

OpenList runs as Docker container `openlist` (image pinned by digest, `--network host`, bound to `127.0.0.1:5244`, `site_url` `…/files`, data in `~/.local/share/openlist`, admin credentials in `~/.local/share/openlist/admin-credentials.txt` mode 0600) with a WebDAV storage `/百度网盘` → `http://127.0.0.1:8080/dav` (username `baidu`; the **password field holds the Baidu Cookie** — that is the "enter your cookie" entry; admin login `admin`/`512557` at `/files/@login`, guest is enabled with full file permissions so browsing needs no login). `tailscale serve` has `/files → 127.0.0.1:5244/files`; the old `/baidu` path was retired. See `DEPLOY.md`.

## Architecture

Under `source/internal/`:
- `cmd/baidupcs-web/main.go` — flags/env + `account` subcommand (`account.go`, a thin HTTP client of `/api/accounts*`; doesn't touch `accounts.json` itself; only for inspecting/cleaning accounts — WebDAV picks the account from the Basic-auth cookie, not from "active").
- `server` — stdlib `ServeMux`; only three route groups: `/api/health`, `/api/accounts*`, `/dav/`. Middleware: recover → redacted leveled logging. `NewWithEngine` injects a stub `xengine.Engine` for tests/smoke (package-level `engineOverride`, restored after construction).
- `davfs` — WebDAV. If `Config.Auth` (an `xengine.AccountResolver`, real engine only) is set, every request needs HTTP Basic auth whose **password is the Baidu Cookie**; `EnsureAccount` maps it to a uid (hash of BDUSS, registers it in `accounts.json` without touching the global active account) and the uid travels in `ctx` (`xengine.WithAccount`; `activeFor(ctx)` picks the pcs instance; `CarryAccount` keeps it for the detached upload goroutine). Dir/meta caches are per account. Stub engines don't implement the resolver → no auth. `FS` implements `webdav.FileSystem` over `xengine.Engine` for PROPFIND/MKCOL/DELETE/MOVE (10s dir + meta cache, `fileInfo` implements `ContentTyper`/`ETager` so x/net/webdav never opens files to sniff types). `handler.go` intercepts GET/HEAD (stream via `xfer.Proxy.ServeDownloadTrusted`, no signature), PUT (body → `tempmgr` file → `xfer.Manager.EnqueueUploadWithRename` → waits for terminal state so errors reach the client; policy always `overwrite`), COPY (`Engine.Copy`, same-name only → else 501).
- `xengine` — **the only package allowed to import `github.com/qjfoidnh/*`**. Reimplements multi-account credentials (`accounts.json`) and the `BaiduPCS` instance pool (upstream `internal/pcsconfig` isn't importable). The `Engine` interface still contains share/offline/recycle/transfer methods though no route uses them now (kept for future re-exposure).
- `xfer` — upload queue (`max_upload_load`), download semaphore (`max_download_load`), streaming proxy (not written to disk; Range pass-through, 3 Range retries on broken streams; Baidu dlinks require the `NetdiskUA` User-Agent). `ServeDownload` verifies an HMAC token, `ServeDownloadTrusted` skips it (WebDAV only). dlink resolver is an injected function field (`ProxyConfig.Resolve`).
- `tempmgr` — upload temp files: 2GB disk watermark, deleted ≤10s after the task's terminal state, orphan sweep.
- `session` — only HMAC signing of download tokens (`SignToken`/`VerifyToken`, key = `config.SessionKey`). `config` / `taskdb` — JSON files (0600, mutex-serialized).

## Conventions and gotchas

- Interface-first design: package-level interfaces are the contract; implementations in `*_impl.go`. Don't import upstream outside `xengine`.
- Error semantics: engine `ErrAuth` ("no Baidu account linked") → `409 BAIDU_NOT_LINKED` on `/api/*`, 409 on WebDAV.
- Tests use stubs injected through config/struct fields, never struct-embedding method overrides (inner receivers ignore outer overrides). Stub Baidu responses must include both `errno` and `error_code`. Tests that hit the network must tighten client timeouts (upstream default is 50s).
- IPv6: `xengine.applyV4FirstDial` pre-binds v4 via the upstream hook `requester.SetTCPHostBind` when v6 to Baidu is unreachable (or `BPW_FORCE_V4=1`). Prefer upstream's exported hooks over overriding the client transport.
- Comments are mostly Chinese and reference FR/NFR/wave IDs from `docs/`; match that style.
- Git: one commit per phase/task, stage only task files (no `git add -A`); push only when the user authorizes. Planned: Docker packaging (publish only to `127.0.0.1` / container network, `BPW_LISTEN=0.0.0.0:8080` inside the container).
