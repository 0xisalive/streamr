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

// TorznabSource queries a Torznab-compatible endpoint. This is the primary
// lever for MAXIMIZING torrent sources: point it at a Prowlarr or Jackett
// aggregator and a single TorznabSource transparently fans out to every
// indexer you've configured there (hundreds of public + private trackers).
// You can also register multiple TorznabSource instances for redundancy.
//
// Torznab is an extension of the Newznab API; the Newznab client below reuses
// the same wire format with a different result shape, so Usenet and torrents
// are searched through one uniform mechanism.
type TorznabSource struct {
	name    string
	baseURL string // e.g. http://prowlarr:9696/1/api  OR a single Jackett indexer
	apiKey  string
	client  *http.Client
	enabled bool
}

func NewTorznabSource(name, baseURL, apiKey string, enabled bool) *TorznabSource {
	return &TorznabSource{
		name:    name,
		baseURL: strings.TrimRight(baseURL, "/"),
		apiKey:  apiKey,
		client:  &http.Client{Timeout: 30 * time.Second},
		enabled: enabled && baseURL != "",
	}
}

func (t *TorznabSource) Name() string       { return t.name }
func (t *TorznabSource) Protocol() Protocol { return ProtocolTorrent }
func (t *TorznabSource) Enabled() bool      { return t.enabled }

func (t *TorznabSource) Search(ctx context.Context, q Query) ([]Candidate, error) {
	u, err := t.buildURL(q)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	resp, err := t.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return nil, fmt.Errorf("torznab %s: status %d: %s", t.name, resp.StatusCode, body)
	}
	return t.parse(resp.Body)
}

// buildURL constructs a Torznab query. ID-based search (imdbid/tmdbid) is used
// when available because it dramatically improves hit-rate for obscure titles
// where fuzzy title matching fails.
func (t *TorznabSource) buildURL(q Query) (string, error) {
	v := url.Values{}
	v.Set("apikey", t.apiKey)
	switch q.Type {
	case MediaEpisode:
		v.Set("t", "tvsearch")
		if q.Season > 0 {
			v.Set("season", strconv.Itoa(q.Season))
		}
		if q.Episode > 0 {
			v.Set("ep", strconv.Itoa(q.Episode))
		}
	default:
		v.Set("t", "movie")
	}
	switch {
	case q.ImdbID != "":
		v.Set("imdbid", strings.TrimPrefix(q.ImdbID, "tt"))
	case q.TmdbID != 0:
		v.Set("tmdbid", strconv.Itoa(q.TmdbID))
	}
	// Always include a text query as fallback alongside IDs.
	if q.Title != "" {
		v.Set("q", q.Title)
	}
	return t.baseURL + "?" + v.Encode(), nil
}

// --- Torznab/Newznab RSS wire format ---

type torznabRSS struct {
	Channel struct {
		Items []torznabItem `xml:"item"`
	} `xml:"channel"`
}

type torznabItem struct {
	Title     string        `xml:"title"`
	Link      string        `xml:"link"`
	PubDate   string        `xml:"pubDate"`
	Enclosure torznabEnc    `xml:"enclosure"`
	Attrs     []torznabAttr `xml:"attr"`
}

type torznabEnc struct {
	URL    string `xml:"url,attr"`
	Length int64  `xml:"length,attr"`
	Type   string `xml:"type,attr"`
}

type torznabAttr struct {
	Name  string `xml:"name,attr"`
	Value string `xml:"value,attr"`
}

func (t *TorznabSource) parse(r io.Reader) ([]Candidate, error) {
	var feed torznabRSS
	if err := xml.NewDecoder(r).Decode(&feed); err != nil {
		return nil, fmt.Errorf("torznab %s: decode: %w", t.name, err)
	}
	out := make([]Candidate, 0, len(feed.Channel.Items))
	for _, it := range feed.Channel.Items {
		c := Candidate{
			Source:    "torznab:" + t.name,
			Protocol:  ProtocolTorrent,
			Title:     it.Title,
			SizeBytes: it.Enclosure.Length,
		}
		// The enclosure URL is a magnet or a .torrent depending on indexer.
		if strings.HasPrefix(it.Enclosure.URL, "magnet:") {
			c.Magnet = it.Enclosure.URL
		} else {
			c.TorrURL = it.Enclosure.URL
		}
		for _, a := range it.Attrs {
			switch a.Name {
			case "seeders":
				c.Seeders = atoi(a.Value)
			case "peers", "leechers":
				c.Leechers = atoi(a.Value)
			case "grabs":
				c.Grabs = atoi(a.Value)
			case "infohash":
				c.Infoy = a.Value
			case "magneturl":
				if c.Magnet == "" {
					c.Magnet = a.Value
				}
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
		ParseReleaseAttrs(&c) // fills Resolution/VideoCodec/Container/Group
		out = append(out, c)
	}
	return out, nil
}

func atoi(s string) int     { n, _ := strconv.Atoi(strings.TrimSpace(s)); return n }
func atoi64(s string) int64 { n, _ := strconv.ParseInt(strings.TrimSpace(s), 10, 64); return n }
