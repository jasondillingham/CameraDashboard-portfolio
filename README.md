# CameraDashboard

**A multi-branch live NVR camera viewer for a small distribution company. Go + go2rtc + LDAP auth, deployed across HQ + 9 branch locations.**

> 📺 **Active production deployment.** Runs daily across HQ + 9 branch locations, exposing live NVR feeds via WebRTC to authenticated employees. This repo is a sanitized public mirror of the internal version — branch names, IPs, credentials, and proprietary details have been replaced with generic placeholders (HQ, Branch A-F).

## Why this was worth building

The motivating problem: a distribution company with **10 branch locations** had a Hikvision NVR per site, but no unified way to view feeds remotely. Each NVR's web UI required a separate login, only worked from inside that branch's LAN, and provided a poor user experience for managers who needed to spot-check operations remotely or across sites.

Commercial unified-camera platforms exist (Milestone, Genetec, Avigilon) but they're priced for enterprise budgets and require extensive vendor lock-in. For a small organization across 10 sites, the math doesn't work.

This is the in-house alternative: a thin Go web app that authenticates against the existing Active Directory, registers RTSP feeds with [go2rtc](https://github.com/AlexxIT/go2rtc) on demand, proxies WebRTC streams to authenticated browsers, and exposes a simple multi-camera dashboard with per-camera permissions, recording playback, and clip export.

## Highlights

- **WebRTC streaming via go2rtc** — RTSP-to-WebRTC relay with on-demand stream registration. Streams only cost CPU when someone's actually viewing them; idle NVRs are not polled.
- **LDAP auth against existing AD** — single sign-on with the company's Active Directory. No separate user management or password handling.
- **Per-camera permission model** — granular access control with admin impersonation. The shipping manager sees the shipping camera, not the executive parking lot.
- **Recording playback with auto-advance** — playhead tracker polls go2rtc's `/api/streams` API to detect end-of-segment via stalled byte counters. Wall-clock detection alone is unreliable because Hikvision NVRs keep the RTSP connection open after playback content finishes.
- **MP4 clip export** — FFmpeg-based clip export with H.265 → H.264 transcoding (`-c:v libx264 -preset fast -crf 23 -movflags +faststart`) and progress streaming to the browser.
- **Admin dashboard** — session monitoring, access logging, per-camera permission audits, NVR health, usage stats.
- **Multi-branch architecture** — each branch runs a local go2rtc instance for its own NVRs; the central app aggregates across branches via Tailscale-style mesh networking, since full-quality RTSP doesn't traverse VPN well.

## Architecture

```
Browser ─── HTTPS ─── CameraDashboard (Go + chi + html/template)
                          │
                          ├── LDAP ──── Active Directory
                          ├── MSSQL ─── ERP user lookup
                          ├── SQLite ── permissions, sessions, audit logs
                          │
                          └── go2rtc (HQ instance) ── 4 HQ NVRs (16-32 ch each)
                                                  ├── Branch A go2rtc ── Branch A NVR
                                                  ├── Branch B go2rtc ── Branch B NVR
                                                  └── ... (Branches C–F)
```

The application itself runs at HQ. Each remote branch runs a small Linux box with its own local go2rtc instance. CameraDashboard registers stream definitions centrally and proxies them to the browser via WebRTC.

## What's interesting in the code

- **`rtsp/relay.go`** — go2rtc stream lifecycle management (register, list, deregister) with retries, connection pooling, and stale-stream cleanup.
- **`internal/auth/`** — LDAP client with group membership lookup, used for both authentication and permission checks.
- **`db/permission_queries.go`** — per-camera ACL with admin override and impersonation support.
- **`handlers/camera_playback.go`** — recording playback with playhead tracking and auto-advance to the next segment via go2rtc API polling.
- **`handlers/camera_export.go`** — FFmpeg pipeline for clip export with progress streaming via Server-Sent Events.
- **`handlers/go2rtc_proxy.go`** — WebRTC offer/answer proxying for browsers behind the corporate proxy.
- **[`notes.md`](notes.md)** — accumulated research on Hikvision NVR quirks, RTSP edge cases (trailing-slash bug, audio codec "none" handling, main-stream-only recording), go2rtc API behavior, and FFmpeg export gotchas.

## Stack

- **Go** stdlib `net/http` + chi router + `html/template` for the web layer
- **HTMX + Alpine.js + Bootstrap** for UI (CDN-loaded, no build step)
- **SQLite** (`modernc.org/sqlite` — pure-Go, no CGO) for permissions, sessions, and audit logs
- **MSSQL** (read-only) against the ERP for user lookup and metadata
- **LDAP** via `github.com/go-ldap/ldap/v3` for authentication
- **[go2rtc](https://github.com/AlexxIT/go2rtc)** as the RTSP-to-WebRTC relay
- **FFmpeg** for MP4 clip export
- **systemd** for service lifecycle

## Local development

```bash
# Build the binary
make build

# Run with local config (expects ../configs/mssql_config.json + camera_config.json)
./cameradashboard
# Listens on http://localhost:8082
```

Local development requires a `mssql_config.json` and `camera_config.json` in `../configs/` with database, LDAP, and NVR connection details. Templates and static assets are embedded into the binary at build time, but you can override the path for live editing during development.

See [`docs/local-go2rtc-setup.md`](docs/local-go2rtc-setup.md) for the per-branch go2rtc deployment runbook.

## Status

Active production deployment. Daily-driver usage across HQ + 9 branches by managers, dispatchers, and remote operators.

## License

MIT — see [LICENSE](LICENSE).
