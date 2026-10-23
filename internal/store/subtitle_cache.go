package store

import "context"

// GetVTT implements subtitle.SubtitleCache: returns the cached WebVTT path for
// a key if present (and the file reference is still recorded).
func (s *Store) GetVTT(ctx context.Context, key string) (string, bool) {
	e, ok, err := s.GetSubtitle(ctx, key)
	if err != nil || !ok {
		return "", false
	}
	return e.VTTPath, true
}

// PutVTT implements subtitle.SubtitleCache: records a produced WebVTT.
func (s *Store) PutVTT(ctx context.Context, key, lang, engine, vttPath string, confidence float64) {
	_ = s.PutSubtitle(ctx, SubtitleEntry{
		Key:        key,
		Lang:       lang,
		Engine:     engine,
		VTTPath:    vttPath,
		Confidence: confidence,
	})
}
