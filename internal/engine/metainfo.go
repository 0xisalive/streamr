package engine

import (
	"context"
	"fmt"
	"net/http"

	"github.com/anacrolix/torrent/metainfo"
)

// loadMetaInfoFromURL downloads and parses a .torrent file.
func loadMetaInfoFromURL(ctx context.Context, url string) (*metainfo.MetaInfo, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("fetch torrent: status %d", resp.StatusCode)
	}
	mi, err := metainfo.Load(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("parse torrent: %w", err)
	}
	return mi, nil
}
