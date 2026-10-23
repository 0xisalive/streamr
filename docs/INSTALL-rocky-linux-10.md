# Installing streamr on Rocky Linux 10

A complete, copy-paste install guide for **Rocky Linux 10** (x86_64). It covers
every dependency, builds streamr, and sets it up to run as a systemd service.

Rocky Linux 10 uses **dnf** and the **EPEL** + **RPM Fusion** repositories for
the pieces not in the base repos (notably `ffmpeg`). All commands assume a
`sudo`-capable user.

---

## 0. Overview of what you'll install

| Component | Source | Purpose |
|---|---|---|
| Go 1.25+ | Go upstream tarball | build the backend |
| Node.js 20+ + npm | dnf module | build the frontend |
| ffmpeg + ffprobe | RPM Fusion | codec detection, remux, transcode, subtitle audio |
| git, make, gcc | base / dnf | build tooling |
| Prowlarr *(or Jackett)* | container or binary | torrent indexer aggregation |
| faster-whisper *(optional)* | pip | quota-free subtitle generation |
| ffsubsync, alass *(optional)* | pip / cargo | align existing subtitles |

streamr's **torrent client is built in** — you do NOT install qBittorrent.

---

## 1. Enable EPEL and RPM Fusion

ffmpeg is not in Rocky's base repos; RPM Fusion provides it.

```bash
# EPEL (Extra Packages for Enterprise Linux)
sudo dnf install -y epel-release

# Enable the CRB (CodeReady Builder) repo that RPM Fusion depends on
sudo dnf config-manager --set-enabled crb

# RPM Fusion free (for el10)
sudo dnf install -y \
  https://download1.rpmfusion.org/free/el/rpmfusion-free-release-10.noarch.rpm

sudo dnf makecache
```

> If the el10 RPM Fusion URL 404s (release timing), use the generic installer
> from https://rpmfusion.org/Configuration and pick the EL 10 link.

## 2. Build tooling

```bash
sudo dnf install -y git make gcc tar xz
```

## 3. ffmpeg (required)

```bash
sudo dnf install -y ffmpeg ffmpeg-devel
# verify
ffmpeg -version | head -1
ffprobe -version | head -1
```

> **If RPM Fusion is unavailable** (e.g. a locked-down server), use a static
> build instead — no root needed:
> ```bash
> mkdir -p ~/.local/bin && cd /tmp
> curl -LO https://johnvansickle.com/ffmpeg/releases/ffmpeg-release-amd64-static.tar.xz
> tar xf ffmpeg-release-amd64-static.tar.xz
> cp ffmpeg-*-static/ffmpeg ffmpeg-*-static/ffprobe ~/.local/bin/
> echo 'export PATH="$HOME/.local/bin:$PATH"' >> ~/.bashrc && source ~/.bashrc
> ```

## 4. Go 1.25+ (required)

Rocky's packaged Go may lag; install the current toolchain from upstream:

```bash
cd /tmp
GO_VERSION=1.25.1
curl -LO https://go.dev/dl/go${GO_VERSION}.linux-amd64.tar.gz
sudo rm -rf /usr/local/go
sudo tar -C /usr/local -xzf go${GO_VERSION}.linux-amd64.tar.gz
echo 'export PATH=$PATH:/usr/local/go/bin' | sudo tee /etc/profile.d/go.sh
source /etc/profile.d/go.sh
go version   # go version go1.25.1 linux/amd64
```

## 5. Node.js 20+ and npm (required for the frontend)

```bash
# Rocky 10 ships Node via dnf modules; pick the 20.x (or newer LTS) stream
sudo dnf module install -y nodejs:20/common
node --version   # v20.x
npm --version
```

## 6. Optional: subtitle tooling

streamr generates subtitles from audio by default (Whisper-on-the-go) and can
also align existing OpenSubtitles files. Install what you want:

```bash
# Python for the whisper tools
sudo dnf install -y python3 python3-pip

# Whisper-on-the-go generation (DEFAULT subtitle path) — pick ONE:
pip3 install --user faster-whisper        # provides whisper-ctranslate2 (recommended, CPU-friendly)
# or
pip3 install --user -U openai-whisper     # provides `whisper`

# Aligners for existing subtitles (optional):
pip3 install --user ffsubsync

# alass (optional, split-aware aligner) via cargo:
sudo dnf install -y cargo
cargo install alass-cli
# add cargo bins to PATH if needed:
echo 'export PATH="$HOME/.cargo/bin:$PATH"' >> ~/.bashrc && source ~/.bashrc
```

Make sure `~/.local/bin` and `~/.cargo/bin` are on `PATH` so streamr can find
these CLIs:

```bash
echo 'export PATH="$HOME/.local/bin:$HOME/.cargo/bin:$PATH"' >> ~/.bashrc
source ~/.bashrc
```

## 7. A torrent indexer: Prowlarr (required for search)

streamr searches torrents through a Torznab endpoint. Prowlarr is the easiest.
On Rocky 10 the clean path is a **container** via Podman (preinstalled on Rocky):

```bash
sudo dnf install -y podman

mkdir -p ~/prowlarr/config
podman run -d --name prowlarr \
  -p 9696:9696 \
  -v ~/prowlarr/config:/config:Z \
  -e PUID=$(id -u) -e PGID=$(id -g) \
  docker.io/linuxserver/prowlarr:latest

# Open http://<host>:9696, add indexers, then copy the API key from
# Settings > General, and note the per-app Torznab URL (http://localhost:9696/1/api).
```

> Prefer Jackett? `podman run -d --name jackett -p 9117:9117 -v ~/jackett:/config:Z docker.io/linuxserver/jackett:latest`

## 8. Build streamr

```bash
git clone https://github.com/0xisalive/streamr.git
cd streamr
make all        # builds web/dist (npm) + bin/streamr (go)
```

`make all` runs `make web` (frontend) then `make build` (backend). You should
get `bin/streamr` and `web/dist/`.

## 9. Configure

```bash
cp config.example.yaml config.yaml
$EDITOR config.yaml
```

Minimum working config:

```yaml
addr: ":8080"
web_dir: "web/dist"
data_dir: "./data"

engine: "embedded"                 # built-in torrent client

tmdb_api_key: "YOUR_TMDB_KEY"      # https://www.themoviedb.org/settings/api

torznab: name=prowlarr url=http://localhost:9696/1/api key=PROWLARR_KEY enabled=true

whisper_gen_enabled: "true"        # needs faster-whisper or openai-whisper on PATH
```

## 10. Run

```bash
./bin/streamr -config config.yaml
# → open http://<host>:8080
```

### Open the firewall (if accessing from another machine)

```bash
sudo firewall-cmd --permanent --add-port=8080/tcp
sudo firewall-cmd --reload
```

---

## 11. Run as a systemd service (recommended)

Install the binary and assets to `/opt/streamr` and run under a dedicated user.

```bash
# dedicated service user
sudo useradd --system --home /opt/streamr --shell /sbin/nologin streamr || true

# lay out files
sudo mkdir -p /opt/streamr
sudo cp bin/streamr /opt/streamr/
sudo cp -r web/dist /opt/streamr/web-dist
sudo cp config.yaml /opt/streamr/
sudo mkdir -p /opt/streamr/data
sudo chown -R streamr:streamr /opt/streamr
```

Point `web_dir` at the installed assets in `/opt/streamr/config.yaml`:

```yaml
web_dir: "/opt/streamr/web-dist"
data_dir: "/opt/streamr/data"
```

Create the unit:

```bash
sudo tee /etc/systemd/system/streamr.service >/dev/null <<'UNIT'
[Unit]
Description=streamr media streaming server
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
User=streamr
Group=streamr
WorkingDirectory=/opt/streamr
ExecStart=/opt/streamr/streamr -config /opt/streamr/config.yaml
Restart=on-failure
RestartSec=5
# Let streamr find ffmpeg/whisper CLIs installed system-wide or in /usr/local/bin
Environment=PATH=/usr/local/bin:/usr/bin:/bin
# Secrets can be injected here instead of the config file:
# Environment=STREAMR_TMDB_API_KEY=...
# Hardening
NoNewPrivileges=true
PrivateTmp=true
ProtectSystem=full

[Install]
WantedBy=multi-user.target
UNIT

sudo systemctl daemon-reload
sudo systemctl enable --now streamr
sudo systemctl status streamr
journalctl -u streamr -f        # follow logs
```

> If you installed whisper/ffsubsync under a user's `~/.local/bin`, either
> install them system-wide or extend the `Environment=PATH=` line to include
> that directory (and ensure it's readable by the `streamr` user).

---

## 12. SELinux note

Rocky Linux 10 ships SELinux in **enforcing** mode. The setup above works
because streamr serves on a normal port (8080) and reads/writes only within
`/opt/streamr`. If you relocate `data_dir` or `web_dir` elsewhere and hit
permission denials, restore contexts:

```bash
sudo restorecon -Rv /opt/streamr
# If serving on a privileged/custom port that SELinux blocks for http:
# sudo semanage port -a -t http_port_t -p tcp 8080   # (needs policycoreutils-python-utils)
```

---

## 13. Quick verification

```bash
curl -s http://localhost:8080/healthz          # {"status":"ok"}
# Search (needs TMDB key):
curl -s "http://localhost:8080/api/search?q=squid+game" | head -c 300
```

Then open the web UI, search for a title, and press **Play (auto-pick best
source)**.

---

## Troubleshooting

| Symptom | Cause / fix |
|---|---|
| `make all` → `cmd/streamr directory not found` | You're on an old checkout; pull latest `main` (this was a packaging bug, now fixed). |
| `ffprobe not found` in logs; mkv/HEVC won't play | ffmpeg not on the service's PATH — see the systemd `Environment=PATH` note. |
| `no indexers configured` warning | Add a `torznab:` line pointing at Prowlarr/Jackett with a valid API key. |
| `subtitles disabled` / no subtitles | Install `faster-whisper` (or `openai-whisper`) and ensure it's on streamr's PATH. |
| Can't reach the UI from another machine | Open port 8080 in `firewalld` (step 10). |
| Permission denied writing `data/` under systemd | `sudo chown -R streamr:streamr /opt/streamr` and check SELinux (`restorecon`). |
