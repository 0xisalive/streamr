package subtitle

// alass is Tier 2 of the alignment pipeline. Unlike ffsubsync's single global
// shift, alass is split-aware: it detects and corrects local/piecewise drift
// introduced by scene cuts, ad breaks, or differing intro lengths between the
// subtitle's reference release and ours. It does not report a numeric
// confidence, so a clean (exit 0) run is treated as a solid-but-not-
// authoritative 0.7.

import (
	"context"
	"os/exec"
)

// AlassEngine wraps the `alass` (a.k.a. `alass-cli`) CLI.
type AlassEngine struct{}

// Name identifies the engine/tier.
func (AlassEngine) Name() string { return "alass" }

// Available reports whether alass (under either common binary name) is on PATH.
func (AlassEngine) Available() bool {
	if _, err := exec.LookPath("alass"); err == nil {
		return true
	}
	if _, err := exec.LookPath("alass-cli"); err == nil {
		return true
	}
	return false
}

// Sync runs alass with the reference media (video preferred, else audio), the
// input subtitle, and the output path. alass emits no score, so a successful
// run returns a fixed confidence of 0.7; a non-zero exit returns (0, err).
func (AlassEngine) Sync(ctx context.Context, req SyncRequest) (float64, error) {
	ref := req.VideoPath
	if ref == "" {
		ref = req.AudioPath
	}

	bin := "alass"
	if _, err := exec.LookPath(bin); err != nil {
		bin = "alass-cli"
	}

	cmd := exec.CommandContext(ctx, bin, ref, req.SubPath, req.OutPath)
	if err := cmd.Run(); err != nil {
		return 0, err
	}
	return 0.7, nil
}
