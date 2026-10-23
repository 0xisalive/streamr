package source

import (
	"context"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// NewznabSource queries a Newznab (Usenet) indexer. Usenet is the highest-value
// addition for OBSCURE / OLD content: because articles live on retention
// servers rather than in a peer swarm, a result that exists is reliably
// downloadable regardless of popularity — no "0 seeders / dead torrent" case.
//
// It shares the RSS wire format with Torznab, so results slot into the same
// Resolver uniformly; only the locator (NZB URL) and health signals differ.
type NewznabSource struct {
	name    string
	baseURL string // e.g. https://indexer.example/api
	apiKey  string
	client  *http.Client
	enabled bool
}

func NewNewznabSource(name, baseURL, apiKey string, enabled bool) *NewznabSource {
	return &NewznabSource{
		name:    name,
		baseURL: strings.TrimRight(baseURL, "/"),
		apiKey:  apiKey,
		client:  &http.Client{Timeout: 30 * time.Second},
		enabled: enabled && baseURL != "" && apiKey != "",
	}
}

func (n *NewznabSource) Name() string       { return n.name }
func (n *NewznabSource) Protocol() Protocol { return ProtocolUsenet }
func (n *NewznabSource) Enabled() bool      { return n.enabled }

func (n *NewznabSource) Search(ctx context.Context, q Query) ([]Candidate, error) {
	v := url.Values{}
	v.Set("apikey", n.apiKey)
	v.Set("o", "xml")
	if q.Type == MediaEpisode {
		v.Set("t", "tvsearch")
		if q.Season > 0 {
			v.Set("season", strconv.Itoa(q.Season))
		}
		if q.Episode > 0 {
			v.Set("ep", strconv.Itoa(q.Episode))
		}
	} else {
		v.Set("t", "movie")
	}
	if q.ImdbID != "" {
		v.Set("imdbid", strings.TrimPrefix(q.ImdbID, "tt"))
	}
	if q.Title != "" {
		v.Set("q", q.Title)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, n.baseURL+"?"+v.Encode(), nil)
	if err != nil {
		return nil, err
	}
	resp, err := n.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return nil, fmt.Errorf("newznab %s: status %d: %s", n.name, resp.StatusCode, body)
	}

	var feed torznabRSS // identical RSS shape
	if err := xml.NewDecoder(resp.Body).Decode(&feed); err != nil {
		return nil, fmt.Errorf("newznab %s: decode: %w", n.name, err)
	}
	out := make([]Candidate, 0, len(feed.Channel.Items))
	for _, it := range feed.Channel.Items {
		c := Candidate{
			Source:    "newznab:" + n.name,
			Protocol:  ProtocolUsenet,
			Title:     it.Title,
			NZBURL:    it.Enclosure.URL,
			SizeBytes: it.Enclosure.Length,
		}
		for _, a := range it.Attrs {
			switch a.Name {
			case "grabs":
				c.Grabs = atoi(a.Value)
			case "size":
				if c.SizeBytes == 0 {
					c.SizeBytes = atoi64(a.Value)
				}
			}
		}
		if ts, err := time.Parse(time.RFC1123Z, it.PubDate); err == nil {
			c.Published = ts
			c.Age = time.Since(ts)
		}
		ParseReleaseAttrs(&c)
		out = append(out, c)
	}
	return out, nil
}
