// Package engine contains streaming engines that turn a chosen source
// Candidate into a seekable, Range-readable Stream while it is still being
// fetched. The qBittorrent engine drives an existing qBittorrent daemon via its
// WebUI API — the lowest-effort path to reliable swarm networking — adding the
// torrent with SEQUENTIAL download enabled so pieces arrive in playback order,
// then serving the on-disk (partial) file through an os.File that the stream
// endpoint seeks over for HTTP Range requests.
package engine

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/0xisalive/streamr/internal/source"
)

// QBittorrent implements source.Engine using the qBittorrent WebUI API v2.
type QBittorrent struct {
	base    string
	user    string
	pass    string
	saveDir string // qBittorrent's download dir, readable by streamr
	http    *http.Client
}

func NewQBittorrent(baseURL, user, pass, saveDir string) (*QBittorrent, error) {
	jar, err := cookiejar.New(nil)
	if err != nil {
		return nil, err
	}
	return &QBittorrent{
		base:    strings.TrimRight(baseURL, "/"),
		user:    user,
		pass:    pass,
		saveDir: saveDir,
		http:    &http.Client{Jar: jar, Timeout: 30 * time.Second},
	}, nil
}

func (q *QBittorrent) Protocols() []source.Protocol {
	return []source.Protocol{source.ProtocolTorrent}
}

// login authenticates against the WebUI; the session cookie is stored in the
// client's jar for subsequent calls.
func (q *QBittorrent) login(ctx context.Context) error {
	form := url.Values{"username": {q.user}, "password": {q.pass}}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		q.base+"/api/v2/auth/login", strings.NewReader(form.Encode()))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Referer", q.base)
	resp, err := q.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK || !strings.Contains(string(body), "Ok") {
		return fmt.Errorf("qbittorrent login failed: status=%d body=%q", resp.StatusCode, body)
	}
	return nil
}

// Open adds the torrent (sequential download on), waits for metadata and the
// largest video file to appear on disk, then returns a Stream backed by that
// file. The returned Stream.Close removes the torrent from qBittorrent.
func (q *QBittorrent) Open(ctx context.Context, c source.Candidate) (*source.Stream, error) {
	if err := q.login(ctx); err != nil {
		return nil, err
	}
	hash, err := q.addTorrent(ctx, c)
	if err != nil {
		return nil, err
	}

	// Wait for metadata so the file list is known.
	files, savePath, err := q.waitForFiles(ctx, hash, 60*time.Second)
	if err != nil {
		return nil, err
	}
	videoIdx, videoFile := largestVideo(files)
	if videoFile == "" {
		return nil, fmt.Errorf("qbittorrent: no video file in torrent %s", hash)
	}

	// Prioritize: deselect everything, then set the video file to max priority
	// and keep sequential download so bytes arrive front-to-back.
	_ = q.setFilePriorities(ctx, hash, len(files), videoIdx)
	_ = q.setSequential(ctx, hash)

	fullPath := resolvePath(q.saveDir, savePath, videoFile)
	rs, size, err := q.openPartial(ctx, fullPath, 60*time.Second)
	if err != nil {
		return nil, err
	}

	return &source.Stream{
		ID:     hash,
		Name:   filepath.Base(videoFile),
		Size:   size,
		Reader: rs,
		NewReader: func() (source.ReadSeekraleCloser, error) {
			// Independent *os.File per request so concurrent Range reads don't
			// share a seek offset.
			return os.Open(fullPath)
		},
		Progress: func() float64 {
			p, _ := q.progress(context.Background(), hash)
			return p
		},
		Close: func() error {
			_ = rs.Close()
			return q.deleteTorrent(context.Background(), hash)
		},
	}, nil
}

func (q *QBittorrent) addTorrent(ctx context.Context, c source.Candidate) (string, error) {
	form := url.Values{}
	switch {
	case c.Magnet != "":
		form.Set("urls", c.Magnet)
	case c.TorrURL != "":
		form.Set("urls", c.TorrURL)
	default:
		return "", fmt.Errorf("qbittorrent: candidate has no magnet or torrent url")
	}
	form.Set("sequentialDownload", "true")
	form.Set("firstLastPiecePrio", "true") // helps player seek to the end quickly
	if q.saveDir != "" {
		form.Set("savepath", q.saveDir)
	}

	if err := q.post(ctx, "/api/v2/torrents/add", form); err != nil {
		return "", err
	}
	// qBittorrent's add endpoint does not return the hash; derive it from the
	// magnet if possible, else poll the torrent list for the newest match.
	if h := infohashFromMagnet(c.Magnet); h != "" {
		return h, nil
	}
	return q.newestHash(ctx)
}

type qbFile struct {
	Name     string  `json:"name"`
	Size     int64   `json:"size"`
	Priority int     `json:"priority"`
	Progress float64 `json:"progress"`
}

func (q *QBittorrent) waitForFiles(ctx context.Context, hash string, timeout time.Duration) ([]qbFile, string, error) {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		files, err := q.files(ctx, hash)
		if err == nil && len(files) > 0 {
			save, _ := q.savePath(ctx, hash)
			return files, save, nil
		}
		select {
		case <-ctx.Done():
			return nil, "", ctx.Err()
		case <-time.After(1 * time.Second):
		}
	}
	return nil, "", fmt.Errorf("qbittorrent: timed out waiting for metadata of %s", hash)
}

func (q *QBittorrent) files(ctx context.Context, hash string) ([]qbFile, error) {
	var files []qbFile
	if err := q.getJSON(ctx, "/api/v2/torrents/files?hash="+hash, &files); err != nil {
		return nil, err
	}
	return files, nil
}

func (q *QBittorrent) savePath(ctx context.Context, hash string) (string, error) {
	var list []struct {
		Hash     string `json:"hash"`
		SavePath string `json:"save_path"`
	}
	if err := q.getJSON(ctx, "/api/v2/torrents/info?hashes="+hash, &list); err != nil {
		return "", err
	}
	if len(list) > 0 {
		return list[0].SavePath, nil
	}
	return "", nil
}

func (q *QBittorrent) progress(ctx context.Context, hash string) (float64, error) {
	var list []struct {
		Progress float64 `json:"progress"`
	}
	if err := q.getJSON(ctx, "/api/v2/torrents/info?hashes="+hash, &list); err != nil {
		return 0, err
	}
	if len(list) > 0 {
		return list[0].Progress, nil
	}
	return 0, nil
}

func (q *QBittorrent) setSequential(ctx context.Context, hash string) error {
	return q.post(ctx, "/api/v2/torrents/toggleSequentialDownload", url.Values{"hashes": {hash}})
}

func (q *QBittorrent) setFilePriorities(ctx context.Context, hash string, total, keepIdx int) error {
	// Priority 0 = do not download; 7 = maximal. Deselect all, then enable the
	// one video file we intend to stream.
	ids := make([]string, 0, total)
	for i := 0; i < total; i++ {
		ids = append(ids, fmt.Sprint(i))
	}
	_ = q.post(ctx, "/api/v2/torrents/filePrio", url.Values{
		"hash": {hash}, "id": {strings.Join(ids, "|")}, "priority": {"0"},
	})
	return q.post(ctx, "/api/v2/torrents/filePrio", url.Values{
		"hash": {hash}, "id": {fmt.Sprint(keepIdx)}, "priority": {"7"},
	})
}

func (q *QBittorrent) deleteTorrent(ctx context.Context, hash string) error {
	return q.post(ctx, "/api/v2/torrents/delete", url.Values{
		"hashes": {hash}, "deleteFiles": {"true"},
	})
}

func (q *QBittorrent) newestHash(ctx context.Context) (string, error) {
	var list []struct {
		Hash    string `json:"hash"`
		AddedOn int64  `json:"added_on"`
	}
	if err := q.getJSON(ctx, "/api/v2/torrents/info?sort=added_on&reverse=true", &list); err != nil {
		return "", err
	}
	if len(list) == 0 {
		return "", fmt.Errorf("qbittorrent: no torrents found after add")
	}
	return list[0].Hash, nil
}

// --- HTTP helpers ---

func (q *QBittorrent) post(ctx context.Context, path string, form url.Values) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, q.base+path, strings.NewReader(form.Encode()))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Referer", q.base)
	resp, err := q.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	io.Copy(io.Discard, resp.Body)
	if resp.StatusCode >= 300 {
		return fmt.Errorf("qbittorrent POST %s: status %d", path, resp.StatusCode)
	}
	return nil
}

func (q *QBittorrent) getJSON(ctx context.Context, path string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, q.base+path, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Referer", q.base)
	resp, err := q.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("qbittorrent GET %s: status %d", path, resp.StatusCode)
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

// --- file/stream helpers ---

var videoExts = map[string]bool{
	".mp4": true, ".mkv": true, ".avi": true, ".m4v": true, ".webm": true, ".mov": true,
}

func largestVideo(files []qbFile) (int, string) {
	best, bestSize, bestIdx := "", int64(-1), -1
	// Sort a copy by size desc so the first video wins deterministically.
	idx := make([]int, len(files))
	for i := range files {
		idx[i] = i
	}
	sort.SliceStable(idx, func(a, b int) bool { return files[idx[a]].Size > files[idx[b]].Size })
	for _, i := range idx {
		ext := strings.ToLower(filepath.Ext(files[i].Name))
		if videoExts[ext] && files[i].Size > bestSize {
			best, bestSize, bestIdx = files[i].Name, files[i].Size, i
			break
		}
	}
	return bestIdx, best
}

func resolvePath(configuredSaveDir, reportedSave, file string) string {
	base := reportedSave
	if base == "" {
		base = configuredSaveDir
	}
	return filepath.Join(base, file)
}

// openPartial waits for the file to appear on disk, then opens it read-only.
// The returned handle is seekable, which is exactly what HTTP Range serving
// needs; reads past the downloaded frontier will block until qBittorrent has
// fetched those (sequential) bytes.
func (q *QBittorrent) openPartial(ctx context.Context, path string, timeout time.Duration) (source.ReadSeekraleCloser, int64, error) {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if fi, err := os.Stat(path); err == nil {
			f, err := os.Open(path)
			if err != nil {
				return nil, 0, err
			}
			return f, fi.Size(), nil
		}
		select {
		case <-ctx.Done():
			return nil, 0, ctx.Err()
		case <-time.After(1 * time.Second):
		}
	}
	return nil, 0, fmt.Errorf("qbittorrent: file never appeared at %s", path)
}

func infohashFromMagnet(magnet string) string {
	// magnet:?xt=urn:btih:<HASH>&...
	if magnet == "" {
		return ""
	}
	const marker = "urn:btih:"
	i := strings.Index(strings.ToLower(magnet), marker)
	if i < 0 {
		return ""
	}
	rest := magnet[i+len(marker):]
	if amp := strings.IndexAny(rest, "&"); amp >= 0 {
		rest = rest[:amp]
	}
	return strings.ToLower(rest)
}

// ensure bytes import used (buffer reserved for future .torrent upload support)
var _ = bytes.MinRead
