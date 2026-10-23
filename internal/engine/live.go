package engine

import "os"

// liveEnabled gates network-dependent tests behind STREAMR_LIVE=1.
func liveEnabled() bool { return os.Getenv("STREAMR_LIVE") == "1" }
