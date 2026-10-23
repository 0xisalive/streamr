package source

import "testing"

func TestParseReleaseAttrs(t *testing.T) {
	cases := []struct {
		title string
		res   string
		codec string
		group string
	}{
		{"My.Mister.S01E01.1080p.NF.WEB-DL.x264-GRP.mkv", "1080p", "h264", "GRP"},
		{"Squid.Game.2021.2160p.WEB.x265-ABC", "2160p", "h265", "ABC"},
		{"Some.KDrama.720p.HEVC-TEAM", "720p", "h265", "TEAM"},
		{"Old.Movie.4K.AV1-XYZ", "2160p", "av1", "XYZ"},
	}
	for _, c := range cases {
		cand := Candidate{Title: c.title}
		ParseReleaseAttrs(&cand)
		if cand.Resolution != c.res {
			t.Errorf("%s: resolution = %q, want %q", c.title, cand.Resolution, c.res)
		}
		if cand.VideoCodec != c.codec {
			t.Errorf("%s: codec = %q, want %q", c.title, cand.VideoCodec, c.codec)
		}
		if cand.Group != c.group {
			t.Errorf("%s: group = %q, want %q", c.title, cand.Group, c.group)
		}
	}
}

func TestScorer_PlayabilityDominates(t *testing.T) {
	s := DefaultScorer{}
	q := Query{}
	deadButIdeal := Candidate{Protocol: ProtocolTorrent, Seeders: 0, VideoCodec: "h264", Container: "mp4", Resolution: "1080p"}
	healthyButHeavy := Candidate{Protocol: ProtocolTorrent, Seeders: 300, VideoCodec: "h265", Container: "mkv", Resolution: "2160p"}
	if s.Score(q, healthyButHeavy) <= s.Score(q, deadButIdeal) {
		t.Errorf("a healthy swarm must outrank a dead one even if the dead one is codec-ideal")
	}
}

func TestScorer_UsenetReliableForObscure(t *testing.T) {
	s := DefaultScorer{}
	q := Query{}
	usenet := Candidate{Protocol: ProtocolUsenet, VideoCodec: "h264", Container: "mkv", Resolution: "1080p"}
	deadTorrent := Candidate{Protocol: ProtocolTorrent, Seeders: 0, VideoCodec: "h264", Container: "mkv", Resolution: "1080p"}
	if s.Score(q, usenet) <= s.Score(q, deadTorrent) {
		t.Errorf("usenet (retention-backed) must outrank a dead torrent for obscure content")
	}
}
