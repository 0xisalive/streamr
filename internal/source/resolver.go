package source

import (
	"context"
	"sort"
	"strings"
	"sync"
	"time"
)

// Resolver fans a Query out to every enabled Source in parallel, merges and
// de-duplicates the candidates, then health-ranks them. This is where we
// "maximize torrent sources": the more Sources registered, the wider the net.
type Resolver struct {
	sources []Source
	timeout time.Duration
	scorer  Scorer
}

// NewResolver builds a resolver. A per-source timeout prevents one slow
// indexer from stalling the whole search.
func NewResolver(timeout time.Duration, scorer Scorer, sources ...Source) *Resolver {
	if scorer == nil {
		scorer = DefaultScorer{}
	}
	return &Resolver{sources: sources, timeout: timeout, scorer: scorer}
}

// Register adds a source after construction (used when config is loaded lazily).
func (r *Resolver) Register(s Source) { r.sources = append(r.sources, s) }

// Resolve queries all enabled sources concurrently and returns ranked
// candidates (best first). Errors from individual sources are swallowed so a
// single broken indexer never fails the whole search; they are returned
// aggregated for logging.
func (r *Resolver) Resolve(ctx context.Context, q Query) ([]Candidate, []SourceError) {
	var (
		mu   sync.Mutex
		all  []Candidate
		errs []SourceError
		wg   sync.WaitGroup
	)

	for _, s := range r.sources {
		if !s.Enabled() {
			continue
		}
		wg.Add(1)
		go func(s Source) {
			defer wg.Done()
			cctx, cancel := context.WithTimeout(ctx, r.timeout)
			defer cancel()

			cands, err := s.Search(cctx, q)
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				errs = append(errs, SourceError{Source: s.Name(), Err: err})
				return
			}
			all = append(all, cands...)
		}(s)
	}
	wg.Wait()

	deduped := dedup(all)
	for i := range deduped {
		deduped[i].Score = r.scorer.Score(q, deduped[i])
	}
	sort.SliceStable(deduped, func(i, j int) bool {
		return deduped[i].Score > deduped[j].Score
	})
	return deduped, errs
}

// SourceError pairs a failing source with its error for aggregated logging.
type SourceError struct {
	Source string
	Err    error
}

// dedup collapses the same release found on multiple indexers. Torrents are
// keyed by infohash (authoritative); otherwise we fall back to a normalized
// title+size key. When duplicates merge we keep the MAX seeders/grabs seen,
// because different indexers report different swarm views — maximizing sources
// means we also get the best-available health signal for a release.
func dedup(in []Candidate) []Candidate {
	byKey := make(map[string]*Candidate, len(in))
	order := make([]string, 0, len(in))

	for i := range in {
		c := in[i]
		key := dedupKey(c)
		if existing, ok := byKey[key]; ok {
			if c.Seeders > existing.Seeders {
				existing.Seeders = c.Seeders
			}
			if c.Grabs > existing.Grabs {
				existing.Grabs = c.Grabs
			}
			// Prefer a magnet locator if one of the duplicates has it (DHT).
			if existing.Magnet == "" && c.Magnet != "" {
				existing.Magnet = c.Magnet
			}
			existing.Source += "," + c.Source
			continue
		}
		cc := c
		byKey[key] = &cc
		order = append(order, key)
	}

	out := make([]Candidate, 0, len(order))
	for _, k := range order {
		out = append(out, *byKey[k])
	}
	return out
}

func dedupKey(c Candidate) string {
	if c.Protocol == ProtocolTorrent && c.Infoy != "" {
		return "ih:" + strings.ToLower(c.Infoy)
	}
	if c.Protocol == ProtocolIPFS && c.CID != "" {
		return "cid:" + c.CID
	}
	return string(c.Protocol) + ":" + normalizeTitle(c.Title)
}

func normalizeTitle(t string) string {
	t = strings.ToLower(t)
	t = strings.NewReplacer(".", " ", "_", " ", "-", " ").Replace(t)
	return strings.Join(strings.Fields(t), " ")
}
