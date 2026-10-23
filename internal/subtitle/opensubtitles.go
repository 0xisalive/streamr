package subtitle

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"time"
)

// OpenSubtitlesProvider fetches subtitles from the OpenSubtitles REST API
// (api.opensubtitles.com). An API key (free tier available) is required. This
// is the canonical source of the raw, often-misaligned subtitles that the
// alignment pipeline then fixes.
//
// If APIKey is empty the provider reports unavailable via a fetch error, so the
// coordinator degrades gracefully to "no subtitles" rather than failing play.
type OpenSubtitlesProvider struct {
	APIKey    string
	UserAgent string
	http      *http.Client
}

func NewOpenSubtitlesProvider(apiKey, userAgent string) *OpenSubtitlesProvider {
	if userAgent == "" {
		userAgent = "streamr/0.1"
	}
	return &OpenSubtitlesProvider{
		APIKey:    apiKey,
		UserAgent: userAgent,
		http:      &http.Client{Timeout: 30 * time.Second},
	}
}

func (p *OpenSubtitlesProvider) Name() string { return "opensubtitles" }

// Fetch searches for a subtitle, requests a download link, and saves the .srt
// into destDir. The flow mirrors the OpenSubtitles REST API: /subtitles to
// search, then /download to exchange a file_id for a temporary URL.
func (p *OpenSubtitlesProvider) Fetch(ctx context.Context, req FetchRequest, destDir string) (string, error) {
	if p.APIKey == "" {
		return "", fmt.Errorf("opensubtitles: no API key configured")
	}

	// 1. Search.
	q := url.Values{}
	if req.ImdbID != "" {
		q.Set("imdb_id", req.ImdbID)
	} else {
		q.Set("query", req.Title)
	}
	if req.Lang != "" {
		q.Set("languages", req.Lang)
	}
	if req.Season > 0 {
		q.Set("season_number", strconv.Itoa(req.Season))
	}
	if req.Episode > 0 {
		q.Set("episode_number", strconv.Itoa(req.Episode))
	}

	var searchResp struct {
		Data []struct {
			Attributes struct {
				Files []struct {
					FileID int `json:"file_id"`
				} `json:"files"`
			} `json:"attributes"`
		} `json:"data"`
	}
	if err := p.apiGET(ctx, "https://api.opensubtitles.com/api/v1/subtitles?"+q.Encode(), &searchResp); err != nil {
		return "", err
	}
	if len(searchResp.Data) == 0 || len(searchResp.Data[0].Attributes.Files) == 0 {
		return "", fmt.Errorf("opensubtitles: no results for %q", req.Title)
	}
	fileID := searchResp.Data[0].Attributes.Files[0].FileID

	// 2. Request download link.
	var dlResp struct {
		Link string `json:"link"`
	}
	if err := p.apiPOST(ctx, "https://api.opensubtitles.com/api/v1/download",
		map[string]any{"file_id": fileID}, &dlResp); err != nil {
		return "", err
	}
	if dlResp.Link == "" {
		return "", fmt.Errorf("opensubtitles: empty download link")
	}

	// 3. Download the .srt.
	outPath := filepath.Join(destDir, "raw.srt")
	if err := p.download(ctx, dlResp.Link, outPath); err != nil {
		return "", err
	}
	return outPath, nil
}

func (p *OpenSubtitlesProvider) apiGET(ctx context.Context, u string, out any) error {
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	p.setHeaders(req)
	return p.do(req, out)
}

func (p *OpenSubtitlesProvider) apiPOST(ctx context.Context, u string, body map[string]any, out any) error {
	b, _ := json.Marshal(body)
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, u, bytes.NewReader(b))
	p.setHeaders(req)
	req.Header.Set("Content-Type", "application/json")
	return p.do(req, out)
}

func (p *OpenSubtitlesProvider) setHeaders(req *http.Request) {
	req.Header.Set("Api-Key", p.APIKey)
	req.Header.Set("User-Agent", p.UserAgent)
	req.Header.Set("Accept", "application/json")
}

func (p *OpenSubtitlesProvider) do(req *http.Request, out any) error {
	resp, err := p.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return fmt.Errorf("opensubtitles: status %d: %s", resp.StatusCode, body)
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

func (p *OpenSubtitlesProvider) download(ctx context.Context, link, outPath string) error {
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, link, nil)
	req.Header.Set("User-Agent", p.UserAgent)
	resp, err := p.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("opensubtitles download: status %d", resp.StatusCode)
	}
	f, err := os.Create(outPath)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = io.Copy(f, resp.Body)
	return err
}
