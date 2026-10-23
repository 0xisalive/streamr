package source

import (
	"context"
	"strings"
)

// IPFSSource resolves content-addressed media. Unlike torrents/usenet there is
// no universal "search" for IPFS — you resolve a known CID, optionally via a
// user-maintained catalog (CID <-> title mapping). This is intentionally a
// secondary backend: IPFS has little movie content and variable latency, but a
// local gateway supports HTTP Range natively, so streaming a known CID is the
// simplest path of the three once the content exists.
type IPFSSource struct {
	name    string
	catalog Catalog // optional title -> CID lookups
	enabled bool
}

// Catalog maps a human query to known CIDs. Implementations could be a local
// JSON file, a pinned directory listing, or a community index.
type Catalog interface {
	Lookup(q Query) ([]CIDEntry, error)
}

type CIDEntry struct {
	Title string
	CID   string
	Size  int64
}

func NewIPFSSource(name string, catalog Catalog, enabled bool) *IPFSSource {
	return &IPFSSource{name: name, catalog: catalog, enabled: enabled && catalog != nil}
}

func (i *IPFSSource) Name() string       { return i.name }
func (i *IPFSSource) Protocol() Protocol { return ProtocolIPFS }
func (i *IPFSSource) Enabled() bool      { return i.enabled }

func (i *IPFSSource) Search(_ context.Context, q Query) ([]Candidate, error) {
	entries, err := i.catalog.Lookup(q)
	if err != nil {
		return nil, err
	}
	out := make([]Candidate, 0, len(entries))
	for _, e := range entries {
		c := Candidate{
			Source:    "ipfs:" + i.name,
			Protocol:  ProtocolIPFS,
			Title:     e.Title,
			CID:       e.CID,
			SizeBytes: e.Size,
			Container: containerFromName(e.Title),
		}
		ParseReleaseAttrs(&c)
		out = append(out, c)
	}
	return out, nil
}

func containerFromName(name string) string {
	if i := strings.LastIndex(name, "."); i >= 0 {
		return strings.ToLower(name[i+1:])
	}
	return ""
}
