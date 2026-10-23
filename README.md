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
- ▶️ **One-click "just press play"** — `/api/autoplay` resolves sources, auto-picks
  the best playable release, and starts streaming. Search, press play, done.
- 📦 **Built-in torrent client** — an embedded BitTorrent engine ships *inside*
  the binary. No qBittorrent to install or configure (though you can drive one
  if you prefer). Supports seek via piece reprioritization.
- 🌐 **Maximized torrent coverage** — one Prowlarr/Jackett endpoint fans out to
  hundreds of public *and* private trackers; add as many indexers as you like.
- 📰 **Usenet support** (Newznab) — the best fix for *obscure / old* titles,
  which die on public trackers but live forever on retention servers.
- 🎞️ **Plays anything in the browser** — automatic **direct-play / remux / HLS
  transcode** decision via ffprobe, so mkv/HEVC/AC3 "just works".
- 💬 **Whisper-on-the-go subtitles (default)** — generates subtitles directly
  from the audio, so you are never blocked by OpenSubtitles' ~20/day limit or
  missing subs for obscure titles. When an external subtitle exists, a tiered
  pipeline **aligns** it and **never ships a worse subtitle**. Results are cached.
- 🧩 **Pluggable sources** — torrent, Usenet, and IPFS behind one interface.
- ⚡ **Multi-threaded throughout** — parallel indexer fan-out, concurrent piece
  download, multi-core ffmpeg transcodes, background subtitle jobs, and
  independent goroutine-per-request serving.
- 🧹 **Idle-session GC** — torrents/transcodes are torn down after inactivity.
- 🖥️ **Linux + FreeBSD** — single static binary, no cgo (verified cross-compile).

---

## Prerequisites

**Required:**

| Dependency | Why | Linux | FreeBSD |
|---|---|---|---|
| **Go 1.25+** | build the backend | distro | `pkg install go` |
| **Node 18+** | build the frontend | distro/nvm | `pkg install node npm` |
| **ffmpeg** (incl. ffprobe) | codec detection, remux, transcode, subtitle audio | distro | `pkg install ffmpeg` |
| **Prowlarr** or **Jackett** | indexer aggregation (torrent search) | docker/distro | `pkg install jackett` |

The torrent client is **built in** — you do *not* need to install qBittorrent.

**Optional (improve subtitles):**

| Dependency | Why | Install |
|---|---|---|
| **faster-whisper** *or* **openai-whisper** | Whisper-on-the-go subtitle generation (the default subtitle path) | `pip install faster-whisper` *or* `pip install -U openai-whisper` |
| ffsubsync, alass | align an existing OpenSubtitles file | `pip install ffsubsync`, `cargo install alass-cli` |
| whisperx | authoritative forced-alignment (opt-in) | `pip install whisperx` (heavy; GPU recommended) |

> Without any whisper CLI, streamr still plays everything; it just won't produce
> subtitles unless OpenSubtitles is configured. Installing faster-whisper is the
> one step that makes subtitles work everywhere, quota-free.

**API keys (all free tier):**
- **TMDB** (required for search) — https://www.themoviedb.org/settings/api
- **Prowlarr/Jackett** (required for torrent search) — self-hosted; copy the API key from its settings
- **OpenSubtitles** (optional) — https://www.opensubtitles.com/consumers

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

Minimum to get streaming (note: no torrent-client config needed):

```yaml
addr: ":8080"
web_dir: "web/dist"
data_dir: "./data"

engine: "embedded"               # built-in torrent client; nothing to install

tmdb_api_key: "YOUR_TMDB_KEY"

# One line here aggregates hundreds of trackers via Prowlarr:
torznab: name=prowlarr url=http://localhost:9696/1/api key=PROWLARR_KEY enabled=true

# Quota-free subtitles from audio (needs faster-whisper or openai-whisper on PATH):
whisper_gen_enabled: "true"
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

### 2. Subtitles — "OpenSubtitles is rate-limited and always out of sync"

Two problems: the free OpenSubtitles tier caps at ~20 downloads/day and lacks
obscure titles entirely; and the subtitles it *does* have are usually timed for
a different release. streamr solves both:

**Default: Whisper-on-the-go generation.** streamr transcribes the media's own
audio into subtitles with a Whisper CLI (faster-whisper or openai-whisper).
Because the timing is derived from the audio itself, it is inherently in sync,
needs no external subtitle, and has no daily quota — it works for any title with
an audio track. This is the default path.

**When an external subtitle exists: tiered alignment.** If OpenSubtitles is
configured and returns a result, streamr fetches it and fixes its timing. The
three kinds of misalignment each need a different tool, chained cheapest-first
and **gated on confidence** so it never ships a subtitle *worse* than the input:

| Tier | Tool | Fixes | Cost |
|---|---|---|---|
| 1 | **ffsubsync** (VAD vs. the audio) | constant offset + global framerate drift | seconds |
| 2 | **alass** (split-aware) | local/piecewise shifts (scene cuts, ad breaks) | fast |
| 3 | **whisperx** (forced alignment) — *opt-in* | re-derives timing from audio, authoritative | slow (GPU) |
| — | **confidence gate** | discards any "fix" worse than the input; keeps original | — |

The audio is extracted **once** per session with ffmpeg and reused across the
generator and all aligner tiers. Every produced subtitle is converted to
**WebVTT**, served to the player's `<track>`, and **cached in SQLite** keyed by
`(release, language)` so the expensive work happens only once.

The confidence gate guards a real, documented failure mode: naive
framerate-search can silently stretch an already-correct subtitle into 30–60s of
drift. streamr refuses to apply a low-confidence result.

### 3. Playing mkv/HEVC in the browser — the "container tax"

Browsers natively play H.264/AAC in MP4, but most torrents are mkv/HEVC/AC3.
streamr probes each file with ffprobe and picks the cheapest delivery that will
play (`internal/transcode`):

| Probe result | Delivery | Cost |
|---|---|---|
| H.264/AAC in MP4 | **Direct play** — raw bytes over HTTP Range | none |
| Browser codecs, wrong container (H.264 in mkv) | **Remux** — `-c copy` to fragmented MP4 | cheap |
| Incompatible codec (HEVC/AC3/DTS) | **Transcode** — H.264/AAC as HLS (hls.js) | CPU/GPU |

Transcodes use all CPU cores (`-threads 0`). On FreeBSD, where hardware
acceleration is limited, expect CPU transcode (which caps concurrent HEVC
streams); direct-play and remux are free everywhere.

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
    anacrolix.go          EMBEDDED torrent engine (default): in-process, seek-aware
    qbittorrent.go        qBittorrent WebUI engine (alternative)
    metainfo.go           .torrent fetch helper
  transcode/
    probe.go              ffprobe media inspection
    decide.go             direct-play / remux / transcode decision tree
    transcoder.go         ffmpeg remux (fMP4) + HLS transcode (multi-core)
  subtitle/
    pipeline.go           tiered alignment + confidence gate
    ffsubsync.go          aligner tier 1
    alass.go              aligner tier 2
    whisperx.go           aligner tier 3 (opt-in)
    whispergen.go         Whisper-on-the-go GENERATION (quota-free default)
    ffmpeg.go             audio extraction + SRT→WebVTT
    opensubtitles.go      raw subtitle provider
    coordinator.go        cache → provider+align → generate → cache, async
  store/
    store.go              SQLite cache (pure-Go driver; subtitles + metadata)
  server/
    server.go             HTTP/JSON API, Range streaming, remux/HLS, SPA hosting
    session.go            live sessions + concurrent idle-session GC
web/                    React + Vite + TypeScript SPA (Seerr-style UI)
```

## Testing

```sh
go test ./...                         # unit tests (ffmpeg/network tests skip)
go test -race ./...                   # with the race detector

# Integration tests (synthesize media with ffmpeg; need ffmpeg + ffprobe):
STREAMR_FFMPEG=$(which ffmpeg) STREAMR_FFPROBE=$(which ffprobe) go test ./internal/transcode/

# Live tests against a real swarm / TMDB (opt-in):
STREAMR_LIVE=1 go test ./internal/engine/ ./internal/server/   # streams the CC Sintel torrent
STREAMR_TMDB_API_KEY=... go test ./internal/discovery/         # live TMDB K-drama search
```

The live engine/server tests stream the Creative-Commons **Sintel** torrent end
to end — opening a magnet, selecting the video file, and serving the opening
bytes over HTTP Range (206) before the file is complete.

---

## HTTP API

| Method | Path | Purpose |
|---|---|---|
| GET | `/api/search?q=` | TMDB search |
| GET | `/api/sources?tmdb_id=&type=&season=&episode=` | ranked source candidates |
| POST | `/api/play` | start a session for a chosen candidate index |
| POST | `/api/autoplay` | **zero-config**: resolve + auto-pick best + start |
| GET | `/api/stream/{id}` | video bytes (HTTP Range / 206; remux piped here too) |
| GET | `/api/hls/{id}/{file}` | HLS playlist + segments (transcode delivery) |
| GET | `/api/progress/{id}` | download progress `0..1` |
| GET | `/api/subtitle/{id}` | generated/aligned WebVTT (when ready) |
| GET | `/healthz` | liveness |

`/api/play` and `/api/autoplay` return `{session_id, stream_url, subtitle_url,
delivery}` where `delivery` is `direct`, `remux`, or `transcode` — the frontend
uses native `<video>` for direct/remux and hls.js for transcode.

---

## Status

Implemented and tested end-to-end:

- ✅ Embedded torrent engine (default) + qBittorrent engine, with seek-aware
  piece reprioritization — **verified streaming the live Sintel torrent**.
- ✅ Direct-play / remux (fMP4) / HLS transcode decision + delivery — **verified
  with real ffmpeg** (probe, remux, and HLS segment generation).
- ✅ Whisper-on-the-go subtitle generation (quota-free default) + tiered
  alignment with confidence gate.
- ✅ SQLite caching of subtitles and metadata (pure-Go, cgo-free).
- ✅ Zero-config `/api/autoplay`.
- ✅ Multi-threaded: parallel indexers, concurrent downloads, multi-core
  transcode, background subtitle jobs, goroutine-per-request serving — **tested
  under the race detector**, including concurrent Range requests.
- ✅ Linux + FreeBSD (amd64 + arm64) static cross-compile verified.

Hardened after an independent review (see `semantic-review/`):

- ✅ Embedded engine exposes the on-disk path, so probe → remux/transcode and
  subtitle audio extraction work on the **default** engine (not just qBittorrent).
- ✅ Per-request readers for concurrent Range serving (no shared-seek contention).
- ✅ Session teardown drains in-flight reads before closing (no GC use-after-free).
- ✅ HLS URL handed out only after ffmpeg writes the playlist + first segment.
- ✅ Subtitle endpoint returns 202-until-ready; the client polls and injects the
  track when available (no permanent 404).
- ✅ Per-session temp dirs (HLS, extracted audio) cleaned on teardown; final VTT
  cached in a stable path.
- ✅ Confidence gate hardened: an unverifiable ffsubsync result no longer passes.
- ✅ SPA path-traversal containment; cleartext-qBittorrent warning.

Natural next steps:

- Hardware-accelerated transcode (VAAPI/NVENC on Linux) for more concurrent HEVC streams.
- Per-episode browsing UI for series (the API already takes season/episode).
- A richer IPFS catalog backend.
