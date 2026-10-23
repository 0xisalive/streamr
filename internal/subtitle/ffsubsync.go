package subtitle

// ffsubsync is Tier 1 of the alignment pipeline: it runs VAD (voice-activity
// detection) against the media's audio and corrects a constant time offset plus
// global framerate drift. It is cheap, always-available, and the first thing we
// try. Its known failure mode is silently stretching an already-correct sub, so
// we parse the alignment score it prints on stderr and translate it into a
// confidence the pipeline's gate can reject.

import (
	"bufio"
	"context"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
)

// FfsubsyncEngine wraps the `ffsubsync` CLI.
type FfsubsyncEngine struct{}

// Name identifies the engine/tier.
func (FfsubsyncEngine) Name() string { return "ffsubsync" }

// Available reports whether the ffsubsync CLI is on PATH.
func (FfsubsyncEngine) Available() bool {
	_, err := exec.LookPath("ffsubsync")
	return err == nil
}

// ffsubsync prints lines like "offset seconds: 3.21" and "score: 0.87" to
// stderr. We capture the score to derive confidence.
var ffsubsyncScoreRe = regexp.MustCompile(`(?i)score[:=]\s*([0-9]*\.?[0-9]+)`)

// Sync runs ffsubsync against the reference media (video preferred, else audio)
// and writes the aligned subtitle to req.OutPath. It returns a confidence in
// [0,1] derived from the score ffsubsync reports; on a parse miss it returns a
// modest default of 0.6 on success, and (0, err) on a non-zero exit.
func (FfsubsyncEngine) Sync(ctx context.Context, req SyncRequest) (float64, error) {
	ref := req.VideoPath
	if ref == "" {
		ref = req.AudioPath
	}

	cmd := exec.CommandContext(ctx, "ffsubsync", ref, "-i", req.SubPath, "-o", req.OutPath)

	stderr, err := cmd.StderrPipe()
	if err != nil {
		return 0, err
	}
	if err := cmd.Start(); err != nil {
		return 0, err
	}

	var score float64
	haveScore := false
	sc := bufio.NewScanner(stderr)
	for sc.Scan() {
		line := sc.Text()
		if m := ffsubsyncScoreRe.FindStringSubmatch(line); m != nil {
			if v, perr := strconv.ParseFloat(strings.TrimSpace(m[1]), 64); perr == nil {
				score = v
				haveScore = true
			}
		}
	}

	if err := cmd.Wait(); err != nil {
		return 0, err
	}

	if !haveScore {
		// Could not parse a score, so we cannot verify the sync improved — and
		// ffsubsync's framerate search can silently RUIN an already-correct
		// subtitle. Return a low confidence so the pipeline's gate keeps the
		// original rather than applying an unverifiable result.
		return 0.3, nil
	}
	return normalizeScore(score), nil
}

// normalizeScore maps ffsubsync's reported score into [0,1]. ffsubsync scores
// are typically already fractional (0..1); values outside that range are
// clamped so the confidence gate always receives a well-formed probability.
func normalizeScore(s float64) float64 {
	if s < 0 {
		return 0
	}
	if s > 1 {
		return 1
	}
	return s
}
