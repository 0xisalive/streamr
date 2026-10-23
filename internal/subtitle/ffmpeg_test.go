package subtitle

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSRTToVTT(t *testing.T) {
	dir := t.TempDir()
	srt := filepath.Join(dir, "in.srt")
	vtt := filepath.Join(dir, "out.vtt")

	srtContent := "1\n" +
		"00:00:01,000 --> 00:00:04,000\n" +
		"안녕하세요\n\n" +
		"2\n" +
		"00:00:05,500 --> 00:00:08,250\n" +
		"Hello there\n"
	if err := os.WriteFile(srt, []byte(srtContent), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := SRTToVTT(srt, vtt); err != nil {
		t.Fatalf("SRTToVTT: %v", err)
	}
	out, err := os.ReadFile(vtt)
	if err != nil {
		t.Fatal(err)
	}
	s := string(out)

	if !strings.HasPrefix(s, "WEBVTT\n") {
		t.Errorf("output must start with WEBVTT header, got %q", s[:min(10, len(s))])
	}
	// Timestamp comma must become a period.
	if !strings.Contains(s, "00:00:01.000 --> 00:00:04.000") {
		t.Errorf("timestamp not converted to VTT format; got:\n%s", s)
	}
	if strings.Contains(s, "00:00:01,000") {
		t.Errorf("SRT comma timestamps still present:\n%s", s)
	}
	// Non-ASCII (Korean) subtitle text must be preserved.
	if !strings.Contains(s, "안녕하세요") {
		t.Errorf("unicode subtitle text not preserved")
	}
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
