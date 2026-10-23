package transcode

import "testing"

func TestDecide(t *testing.T) {
	cases := []struct {
		name string
		mi   MediaInfo
		want Mode
	}{
		{"h264/aac in mp4 → direct", MediaInfo{Container: "mov,mp4,m4a,3gp,3g2,mj2", VideoCodec: "h264", AudioCodec: "aac"}, DirectPlay},
		{"h264/aac in mkv → remux", MediaInfo{Container: "matroska,webm", VideoCodec: "h264", AudioCodec: "aac"}, Remux},
		{"hevc/aac in mkv → transcode", MediaInfo{Container: "matroska,webm", VideoCodec: "hevc", AudioCodec: "aac"}, Transcode},
		{"h264/ac3 in mkv → transcode (audio)", MediaInfo{Container: "matroska,webm", VideoCodec: "h264", AudioCodec: "ac3"}, Transcode},
		// A pure webm (no matroska alias) is browser-native → direct.
		{"vp9/opus pure webm → direct", MediaInfo{Container: "webm", VideoCodec: "vp9", AudioCodec: "opus"}, DirectPlay},
		// The "matroska,webm" alias (what .mkv AND ffmpeg-muxed webm report) is
		// treated as remux, since we cannot distinguish a real .webm and
		// remuxing is cheap/harmless. This documents the deliberate trade-off.
		{"matroska,webm alias → remux", MediaInfo{Container: "matroska,webm", VideoCodec: "vp9", AudioCodec: "opus"}, Remux},
		{"av1/aac in mp4 → direct", MediaInfo{Container: "mov,mp4", VideoCodec: "av1", AudioCodec: "aac"}, DirectPlay},
		{"h264/dts in mp4 → transcode (audio only)", MediaInfo{Container: "mov,mp4", VideoCodec: "h264", AudioCodec: "dts"}, Transcode},
	}
	for _, c := range cases {
		if got := Decide(c.mi); got != c.want {
			t.Errorf("%s: Decide = %v, want %v", c.name, got, c.want)
		}
	}
}

func TestNeedsReencode(t *testing.T) {
	hevc := MediaInfo{VideoCodec: "hevc", AudioCodec: "aac"}
	if !NeedsVideoReencode(hevc) {
		t.Error("hevc should need video re-encode")
	}
	if NeedsAudioReencode(hevc) {
		t.Error("aac should not need audio re-encode")
	}
	ac3 := MediaInfo{VideoCodec: "h264", AudioCodec: "ac3"}
	if NeedsVideoReencode(ac3) {
		t.Error("h264 should not need video re-encode")
	}
	if !NeedsAudioReencode(ac3) {
		t.Error("ac3 should need audio re-encode")
	}
}
