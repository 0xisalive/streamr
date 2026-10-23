package source

import (
	"regexp"
	"strings"
)

// ParseReleaseAttrs extracts resolution/codec/container/group from a scene-style
// release title. These feed the scorer (streaming-friendliness) and the
// playback decision tree (direct-play vs remux vs transcode).
func ParseReleaseAttrs(c *Candidate) {
	title := c.Title

	if m := reResolution.FindString(title); m != "" {
		c.Resolution = strings.ToLower(m)
		if c.Resolution == "4k" {
			c.Resolution = "2160p"
		}
	}
	switch {
	case reH265.MatchString(title):
		c.VideoCodec = "h265"
	case reH264.MatchString(title):
		c.VideoCodec = "h264"
	case reAV1.MatchString(title):
		c.VideoCodec = "av1"
	}
	if m := reContainer.FindString(title); m != "" {
		c.Container = strings.ToLower(strings.TrimPrefix(m, "."))
	}
	// Strip a trailing video extension so "-GRP.mkv" yields group "GRP".
	forGroup := reVideoExt.ReplaceAllString(title, "")
	if m := reGroup.FindStringSubmatch(forGroup); len(m) == 2 {
		c.Group = m[1]
	}
}

var (
	reResolution = regexp.MustCompile(`(?i)\b(2160p|1080p|720p|480p|4k)\b`)
	reH265       = regexp.MustCompile(`(?i)\b(x265|h\.?265|hevc)\b`)
	reH264       = regexp.MustCompile(`(?i)\b(x264|h\.?264|avc)\b`)
	reAV1        = regexp.MustCompile(`(?i)\bav1\b`)
	reContainer  = regexp.MustCompile(`(?i)\.(mp4|mkv|avi|m4v|webm)\b`)
	reVideoExt   = regexp.MustCompile(`(?i)\.(mp4|mkv|avi|m4v|webm|mov)$`)
	reGroup      = regexp.MustCompile(`-([A-Za-z0-9]+)$`)
)
