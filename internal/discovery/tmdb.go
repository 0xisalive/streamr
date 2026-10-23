// Package discovery provides the Seerr-style metadata/search layer. Like
// Overseerr/Jellyseerr/Seerr, discovery is backed by TMDB: it supplies search,
// posters, overviews and external IDs (IMDb) — but it never downloads or plays
// anything. The IMDb/TMDB IDs it returns are handed to the source.Resolver for
// ID-based indexer lookups, which is what makes obscure titles findable.
package discovery

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

type Client struct {
	apiKey  string
	baseURL string
	imgBase string
	http    *http.Client
}

func NewClient(apiKey string) *Client {
	return &Client{
		apiKey:  apiKey,
		baseURL: "https://api.themoviedb.org/3",
		imgBase: "https://image.tmdb.org/t/p/w500",
		http:    &http.Client{Timeout: 15 * time.Second},
	}
}

func (c *Client) Enabled() bool { return c.apiKey != "" }

// Result is a unified search hit for movies and TV.
type Result struct {
	TmdbID    int     `json:"tmdb_id"`
	Type      string  `json:"type"` // "movie" | "tv"
	Title     string  `json:"title"`
	Year      int     `json:"year"`
	Overview  string  `json:"overview"`
	PosterURL string  `json:"poster_url"`
	Rating    float64 `json:"rating"`
}

// Search queries TMDB's multi-search and normalizes movie/tv results.
func (c *Client) Search(ctx context.Context, query string) ([]Result, error) {
	v := url.Values{}
	v.Set("api_key", c.apiKey)
	v.Set("query", query)
	v.Set("include_adult", "false")

	var raw struct {
		Results []struct {
			ID           int     `json:"id"`
			MediaType    string  `json:"media_type"`
			Title        string  `json:"title"`
			Name         string  `json:"name"`
			Overview     string  `json:"overview"`
			PosterPath   string  `json:"poster_path"`
			ReleaseDate  string  `json:"release_date"`
			FirstAirDate string  `json:"first_air_date"`
			VoteAverage  float64 `json:"vote_average"`
		} `json:"results"`
	}
	if err := c.get(ctx, "/search/multi", v, &raw); err != nil {
		return nil, err
	}

	out := make([]Result, 0, len(raw.Results))
	for _, r := range raw.Results {
		if r.MediaType != "movie" && r.MediaType != "tv" {
			continue
		}
		title, date := r.Title, r.ReleaseDate
		if r.MediaType == "tv" {
			title, date = r.Name, r.FirstAirDate
		}
		res := Result{
			TmdbID:   r.ID,
			Type:     r.MediaType,
			Title:    title,
			Overview: r.Overview,
			Rating:   r.VoteAverage,
			Year:     yearOf(date),
		}
		if r.PosterPath != "" {
			res.PosterURL = c.imgBase + r.PosterPath
		}
		out = append(out, res)
	}
	return out, nil
}

// ExternalIDs fetches the IMDb id for a TMDB item — passed to indexers for
// reliable ID-based search of obscure titles.
func (c *Client) ExternalIDs(ctx context.Context, tmdbID int, mediaType string) (imdbID string, err error) {
	v := url.Values{}
	v.Set("api_key", c.apiKey)
	var raw struct {
		ImdbID string `json:"imdb_id"`
	}
	path := fmt.Sprintf("/%s/%d/external_ids", mediaType, tmdbID)
	if err := c.get(ctx, path, v, &raw); err != nil {
		return "", err
	}
	return raw.ImdbID, nil
}

func (c *Client) get(ctx context.Context, path string, v url.Values, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+path+"?"+v.Encode(), nil)
	if err != nil {
		return err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("tmdb %s: status %d", path, resp.StatusCode)
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

func yearOf(date string) int {
	if len(date) >= 4 {
		if y, err := strconv.Atoi(date[:4]); err == nil {
			return y
		}
	}
	return 0
}

// NormalizeType maps TMDB's "tv"/"movie" to the resolver's MediaType string.
func NormalizeType(t string) string {
	if strings.EqualFold(t, "tv") {
		return "episode"
	}
	return "movie"
}
