package source

import (
	"context"
	"errors"
	"testing"
	"time"
)

// fakeSource is a test double implementing Source.
type fakeSource struct {
	name    string
	proto   Protocol
	enabled bool
	out     []Candidate
	err     error
	delay   time.Duration
}

func (f *fakeSource) Name() string       { return f.name }
func (f *fakeSource) Protocol() Protocol { return f.proto }
func (f *fakeSource) Enabled() bool      { return f.enabled }
func (f *fakeSource) Search(ctx context.Context, _ Query) ([]Candidate, error) {
	if f.delay > 0 {
		select {
		case <-time.After(f.delay):
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	return f.out, f.err
}

func TestResolver_FanOutAndRank(t *testing.T) {
	dead := Candidate{Source: "a", Protocol: ProtocolTorrent, Title: "Show.S01E01.1080p.x264-GRP", Infoy: "deadhash", Seeders: 0}
	healthy := Candidate{Source: "b", Protocol: ProtocolTorrent, Title: "Show.S01E01.1080p.x264-GRP2", Infoy: "healthyhash", Seeders: 500}

	r := NewResolver(2*time.Second, DefaultScorer{},
		&fakeSource{name: "src1", proto: ProtocolTorrent, enabled: true, out: []Candidate{dead}},
		&fakeSource{name: "src2", proto: ProtocolTorrent, enabled: true, out: []Candidate{healthy}},
		&fakeSource{name: "disabled", proto: ProtocolTorrent, enabled: false, out: []Candidate{{Title: "nope"}}},
	)

	cands, errs := r.Resolve(context.Background(), Query{Title: "Show"})
	if len(errs) != 0 {
		t.Fatalf("unexpected errors: %v", errs)
	}
	if len(cands) != 2 {
		t.Fatalf("want 2 candidates, got %d", len(cands))
	}
	// Healthy swarm must rank above the dead one.
	if cands[0].Infoy != "healthyhash" {
		t.Errorf("want healthy candidate first, got %q (score %.1f) before %q (score %.1f)",
			cands[0].Infoy, cands[0].Score, cands[1].Infoy, cands[1].Score)
	}
	if cands[0].Score <= cands[1].Score {
		t.Errorf("ranking not descending: %.1f <= %.1f", cands[0].Score, cands[1].Score)
	}
}

func TestResolver_ErrorsDoNotFailWholeSearch(t *testing.T) {
	r := NewResolver(2*time.Second, DefaultScorer{},
		&fakeSource{name: "broken", proto: ProtocolTorrent, enabled: true, err: errors.New("boom")},
		&fakeSource{name: "ok", proto: ProtocolTorrent, enabled: true, out: []Candidate{
			{Title: "Good.1080p.x264", Infoy: "h1", Seeders: 10},
		}},
	)
	cands, errs := r.Resolve(context.Background(), Query{Title: "x"})
	if len(cands) != 1 {
		t.Fatalf("want 1 candidate from the healthy source, got %d", len(cands))
	}
	if len(errs) != 1 || errs[0].Source != "broken" {
		t.Fatalf("want 1 error from 'broken', got %v", errs)
	}
}

func TestResolver_RespectsTimeout(t *testing.T) {
	start := time.Now()
	r := NewResolver(100*time.Millisecond, DefaultScorer{},
		&fakeSource{name: "slow", proto: ProtocolTorrent, enabled: true, delay: 5 * time.Second,
			out: []Candidate{{Title: "late"}}},
	)
	_, errs := r.Resolve(context.Background(), Query{Title: "x"})
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Fatalf("resolver did not honor per-source timeout; took %v", elapsed)
	}
	if len(errs) != 1 {
		t.Fatalf("want a timeout error, got %v", errs)
	}
}

func TestDedup_MergesByInfohashKeepsMaxSeeders(t *testing.T) {
	in := []Candidate{
		{Source: "i1", Protocol: ProtocolTorrent, Infoy: "ABC", Seeders: 10, Title: "x"},
		{Source: "i2", Protocol: ProtocolTorrent, Infoy: "abc", Seeders: 99, Title: "x", Magnet: "magnet:?xt=urn:btih:abc"},
	}
	out := dedup(in)
	if len(out) != 1 {
		t.Fatalf("want 1 deduped candidate, got %d", len(out))
	}
	if out[0].Seeders != 99 {
		t.Errorf("want max seeders 99 kept, got %d", out[0].Seeders)
	}
	if out[0].Magnet == "" {
		t.Errorf("want magnet preserved from the duplicate that had it")
	}
}
