# streamr

A self-hosted media app with a **Seerr-style discovery interface** that
**streams torrents (and optionally Usenet / IPFS) Netflix-style** — playback
begins immediately while content downloads in the background, instead of waiting
for the whole file. Runs on **Linux and FreeBSD** as a single static Go binary.

> ⚖️ **Legal note.** streamr is a streaming *engine*; it does not host or provide
> any content. The same way a torrent client is legal but what you download may
> not be, you are responsible for what you point it at. Stream content you have
> the right to: public-domain films, Creative Commons works, the Internet
> Archive, or media you own. Streaming copyrighted material you don't hold a
> license to is infringement in most jurisdictions regardless of the
> "stream vs. download" distinction — the bytes still transit your machine and
> you re-share them as a peer.

---

## How it works

```
Browser (React SPA)                 Go backend (single binary)
 ┌───────────────┐   /api/search    ┌──────────────────────────────────────┐
 │ search / browse│ ───────────────▶ │ Discovery  → TMDB (metadata, posters) │
 │ (Seerr-style)  │   /api/sources   │ Resolver   → fans out to ALL indexers │──▶ Prowlarr / Jackett
 │ source list    │ ───────────────▶ │              (torrent + usenet),      │    (torrent + usenet)
 │                │                  │              dedups, health-ranks     │
 │  ▶ Play        │   /api/play      │ Engine     → qBittorrent WebUI:       │──▶ BitTorrent swarm
 │                │ ───────────────▶ │              add torrent, SEQUENTIAL  │
 │ <video> + <track>  /api/stream    │              download, prioritize     │
 │  HTTP Range    │ ◀──────────────▶ │ Stream     → HTTP 206 Range over the  │
 │                │   /api/subtitle  │              partial file             │
 └───────────────┘ ◀─────────────── │ Subtitles  → fetch + tiered align     │
                                     └──────────────────────────────────────┘
```

Two ideas do the heavy lifting:

1. **Sequential download + HTTP Range.** The torrent is fetched in playback
   order, and the partially-downloaded file is served with `206 Partial
   Content` — exactly what an HTML5 `<video>` element speaks. Seeking re-serves
   from a new byte offset.
2. **Maximized sources, health-ranked.** Every configured indexer is queried in
   parallel; results are de-duplicated (by infohash) and ranked so playable,
   stream-friendly releases float to the top and dead torrents sink.

---

## Features

- 🔎 **Seerr-style discovery** backed by TMDB (search, posters, overviews, IMDb IDs).
- 🌐 **Maximized torrent coverage** — one Prowlarr/Jackett endpoint fans out to
  hundreds of public *and* private trackers; add as many indexers as you like.
- 📰 **Usenet support** (Newznab) — the best fix for *obscure / old* titles,
  which die on public trackers but live forever on retention servers.
- 🧩 **Pluggable sources** — torrent, Usenet, and IPFS behind one interface.
- ▶️ **Instant streaming** — sequential download + HTTP Range, no full download.
- 💬 **Tiered subtitle alignment** — fixes the "OpenSubtitles is always out of
  sync" problem (details below), and **never ships a worse subtitle**.
- 🧹 **Idle-session GC** — torrents are torn down after inactivity to prevent leaks.
- 🖥️ **Linux + FreeBSD** — static binary, no cgo.

---

## Prerequisites

| Dependency | Why | Linux | FreeBSD |
|---|---|---|---|
| **Go 1.25+** | build the backend | `pkg`/distro | `pkg install go` |
| **Node 18+** | build the frontend | distro/nvm | `pkg install node npm` |
| **qBittorrent** (WebUI on) | torrent engine | distro | `pkg install qbittorrent-nox` |
| **ffmpeg** | audio extraction + (future) transcode | distro | `pkg install ffmpeg` |
| **Prowlarr** or **Jackett** | indexer aggregation | docker/distro | `pkg install jackett` |
| ffsubsync, alass | subtitle sync (baseline) | `pip install ffsubsync`, `cargo install alass-cli` | same |
| whisperx *(optional)* | authoritative subtitle sync | `pip install whisperx` | heavy; optional |

**API keys (all free tier):**
- **TMDB** — https://www.themoviedb.org/settings/api
- **OpenSubtitles** — https://www.opensubtitles.com/consumers (only needed for subtitles)
- **Prowlarr/Jackett** — self-hosted; copy the API key from its settings

---

## Install & build

```sh
git clone https://github.com/0xisalive/streamr.git
cd streamr

# Build the frontend and backend (host OS):
make all          # == make web && make build
# → produces bin/streamr and web/dist/

# Or cross-compile release binaries for Linux + FreeBSD:
make dist         # → dist/streamr-{linux-amd64,freebsd-amd64,freebsd-arm64}
```

No manual steps are listed here that touch your system config or credentials
beyond the config file below.

---

## Configure

```sh
cp config.example.yaml config.yaml
$EDITOR config.yaml
```

Minimum to get streaming:

```yaml
addr: ":8080"
web_dir: "web/dist"
data_dir: "./data"

tmdb_api_key: "YOUR_TMDB_KEY"

qbit_url: "http://localhost:8081"
qbit_user: "admin"
qbit_pass: "YOUR_QBIT_PASS"
qbit_save_dir: "/downloads"      # must be readable by streamr

# One line here aggregates hundreds of trackers via Prowlarr:
torznab: name=prowlarr url=http://localhost:9696/1/api key=PROWLARR_KEY enabled=true
```

Secrets can also be set via environment variables (they override the file):
`STREAMR_TMDB_API_KEY`, `STREAMR_QBIT_URL`, `STREAMR_QBIT_USER`,
`STREAMR_QBIT_PASS`, `STREAMR_QBIT_SAVE_DIR`, `STREAMR_OPENSUBTITLES_API_KEY`,
`STREAMR_WHISPERX_ENABLED`, `STREAMR_ADDR`, `STREAMR_WEB_DIR`, `STREAMR_DATA_DIR`.

See **[`config.example.yaml`](config.example.yaml)** for every option with
inline comments.

---

## Run

### 1. Start your supporting services
- **qBittorrent** with the WebUI enabled (*Tools → Options → Web UI*), reachable
  at `qbit_url`. Its download dir must match `qbit_save_dir` and be readable by
  streamr (same host or a shared mount).
- **Prowlarr** (or Jackett) with at least one indexer configured, reachable at
  the `torznab` URL. Add as many trackers there as you want — that's how you
  maximize coverage.

### 2. Start streamr
```sh
./bin/streamr -config config.yaml
# → listening on :8080 (web dir: "web/dist")
```

On FreeBSD, the same command with the FreeBSD binary. (A sample `rc.d` / systemd
unit is trivial to add; streamr handles `SIGINT`/`SIGTERM` for graceful shutdown.)

### 3. Open the app
Browse to **http://localhost:8080** and:
1. **Search** for a movie or show (TMDB-powered).
2. Click a result → streamr queries every indexer and shows a **ranked source
   list** (resolution, codec, container, seeders, score).
3. Click **Play** on a source → streamr adds it to qBittorrent with sequential
   download and starts streaming. Playback begins as soon as the opening bytes
   land.
4. If OpenSubtitles is configured, an **aligned subtitle track** is prepared in
   the background and appears when ready.

---

## Development

Run the backend and the Vite dev server separately (hot reload):

```sh
# terminal 1 — API on :8080
go run ./cmd/streamr -config config.yaml      # leave web_dir empty in config

# terminal 2 — frontend on :5173, proxies /api → :8080
cd web && npm run dev
```

The Vite dev server (`web/vite.config.ts`) proxies `/api` to `localhost:8080`,
so the SPA talks to the live backend during development.

---

## The two hard problems, and how streamr solves them

### 1. Coverage — obscure / non-mainstream titles

Public trackers let niche content go **dead** (0 seeders = unplayable). streamr
attacks this three ways:

- **Aggregate everything.** A single Prowlarr/Jackett `torznab` line fans out to
  hundreds of indexers; register multiple lines for redundancy. The resolver
  queries them concurrently and merges results.
- **ID-based search.** When TMDB yields an IMDb ID, indexers are queried by ID
  rather than fuzzy title text — dramatically better hit-rate on obscure titles.
- **Add Usenet.** Enable a `newznab` indexer. Because Usenet content lives on
  **retention servers** rather than a peer swarm, a result that exists is
  reliably downloadable *regardless of popularity* — the single biggest win for
  old/rare material. It streams via the same sequential-download + Range path.

Ranking (`internal/source/scorer.go`) then pushes **playable, stream-friendly**
releases to the top: healthy swarms and retention-backed Usenet score high, dead
torrents score negative, and browser-native codecs/containers (H.264 in mp4)
outrank HEVC/mkv to avoid a transcode.

### 2. Subtitle alignment — "OpenSubtitles is always out of sync"

Misalignment comes in **three kinds**, and no single tool fixes all three, so
streamr chains them cheapest-first and **gates on confidence** so it never ships
a subtitle *worse* than the original:

| Tier | Tool | Fixes | Cost |
|---|---|---|---|
| 1 | **ffsubsync** (VAD vs. the audio) | constant offset + global framerate drift | seconds |
| 2 | **alass** (split-aware) | local/piecewise shifts (scene cuts, ad breaks) | fast |
| 3 | **whisperx** (forced alignment) — *opt-in* | re-derives timing from audio, authoritative | slow (GPU) |
| — | **confidence gate** | discards any "fix" worse than the input; keeps original | — |

The audio is extracted **once** per session with ffmpeg and reused across tiers.
ffsubsync + alass are the always-on baseline; WhisperX is opt-in
(`whisperx_enabled: true`) because PyTorch is heavy — especially on FreeBSD,
where you'd typically run the baseline only. The aligned subtitle is converted
to **WebVTT** and served to the player's `<track>`.

The gate exists for a real, documented failure mode: naive framerate-search can
silently stretch an already-correct subtitle into 30–60s of drift. streamr
refuses to apply a low-confidence result.

---

## Project layout

```
cmd/streamr/            entrypoint: load config, wire everything, serve
internal/
  config/               config file + env loading
  discovery/            TMDB client (the Seerr-style metadata layer)
  source/               pluggable sources + resolver + health scorer
    source.go             Source/Engine/Stream interfaces
    resolver.go           concurrent fan-out, dedup, ranking
    scorer.go             playability + stream-friendliness ranking
    torznab.go            torrent indexers (Prowlarr/Jackett)  ← maximize coverage
    newznab.go            usenet indexers (obscure-content win)
    ipfs.go               IPFS backend (secondary)
    release.go            parse resolution/codec/container from titles
  engine/
    qbittorrent.go        qBittorrent WebUI engine: sequential DL + Range source
  subtitle/
    pipeline.go           tiered alignment + confidence gate
    ffsubsync.go          tier 1
    alass.go              tier 2
    whisperx.go           tier 3 (opt-in)
    ffmpeg.go             audio extraction + SRT→WebVTT
    opensubtitles.go      raw subtitle provider
    coordinator.go        fetch → align → convert, run async per session
  server/
    server.go             HTTP/JSON API + SPA hosting + Range streaming
    session.go            live sessions + idle-session garbage collection
web/                    React + Vite + TypeScript SPA (Seerr-style UI)
```

---

## HTTP API

| Method | Path | Purpose |
|---|---|---|
| GET | `/api/search?q=` | TMDB search |
| GET | `/api/sources?tmdb_id=&type=&season=&episode=` | ranked source candidates |
| POST | `/api/play` | start a streaming session for a chosen candidate |
| GET | `/api/stream/{id}` | video bytes (HTTP Range / 206) |
| GET | `/api/progress/{id}` | download progress `0..1` |
| GET | `/api/subtitle/{id}` | aligned WebVTT (when ready) |
| GET | `/healthz` | liveness |

---

## Roadmap / known gaps

This is a working scaffold with real, wired paths. Natural next steps:

- **On-the-fly remux/transcode** — `ffmpeg -c copy` to fragmented MP4 for
  H.264-in-mkv, and HLS + hls.js for HEVC/AC3. (Direct-play works today; the
  decision tree is designed for, but the transcode handlers are not yet wired.)
- **Embedded torrent engine** (`anacrolix/torrent`) as an alternative to driving
  qBittorrent, for finer per-piece deadline control on seek.
- **SQLite caching** of metadata and aligned subtitles (keyed by release+sub hash).
- **Seek-aware piece reprioritization** beyond sequential for large scrubs.
- Tests for the resolver ranking and the SRT→VTT conversion.
