import { useEffect, useState } from 'react'
import { useNavigate, useParams } from 'react-router-dom'
import { getSources, play } from '../api.ts'
import type { Candidate, MediaType } from '../api.ts'

function formatSize(bytes: number): string {
  if (!bytes) return '—'
  const units = ['B', 'KB', 'MB', 'GB', 'TB']
  let n = bytes
  let i = 0
  while (n >= 1024 && i < units.length - 1) {
    n /= 1024
    i++
  }
  return `${n.toFixed(n < 10 && i > 0 ? 1 : 0)} ${units[i]}`
}

export default function Title() {
  const { type, id } = useParams<{ type: string; id: string }>()
  const navigate = useNavigate()
  const [candidates, setCandidates] = useState<Candidate[]>([])
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState<string | null>(null)
  const [playingIndex, setPlayingIndex] = useState<number | null>(null)

  const mediaType: MediaType = type === 'tv' ? 'tv' : 'movie'
  const tmdbId = Number(id)

  useEffect(() => {
    let cancelled = false
    setLoading(true)
    setError(null)
    getSources({ tmdb_id: tmdbId, type: mediaType })
      .then((res) => {
        if (!cancelled) setCandidates(res)
      })
      .catch((err: unknown) => {
        if (!cancelled)
          setError(err instanceof Error ? err.message : 'Failed to load sources')
      })
      .finally(() => {
        if (!cancelled) setLoading(false)
      })
    return () => {
      cancelled = true
    }
  }, [tmdbId, mediaType])

  async function onPlay(index: number) {
    setPlayingIndex(index)
    setError(null)
    try {
      const res = await play({
        candidate_index: index,
        tmdb_id: tmdbId,
        type: mediaType,
        season: 0,
        episode: 0,
      })
      navigate('/watch', {
        state: {
          stream_url: res.stream_url,
          subtitle_url: res.subtitle_url,
          session_id: res.session_id,
        },
      })
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Failed to start playback')
      setPlayingIndex(null)
    }
  }

  return (
    <div className="title-page">
      <button className="back" onClick={() => navigate(-1)}>
        ← Back
      </button>
      <h1>Sources</h1>
      <p className="subtitle">
        {mediaType.toUpperCase()} · TMDB #{tmdbId}
      </p>

      {error && <p className="error">{error}</p>}
      {loading && <p className="empty">Loading sources…</p>}

      {!loading && candidates.length === 0 && !error && (
        <p className="empty">No sources found.</p>
      )}

      {candidates.length > 0 && (
        <table className="sources">
          <thead>
            <tr>
              <th>Source</th>
              <th>Protocol</th>
              <th>Resolution</th>
              <th>Codec</th>
              <th>Container</th>
              <th>Size</th>
              <th>Seeders</th>
              <th>Score</th>
              <th></th>
            </tr>
          </thead>
          <tbody>
            {candidates.map((c, i) => (
              <tr key={i}>
                <td title={c.title}>{c.source}</td>
                <td>
                  <span className={`badge badge-${c.protocol}`}>
                    {c.protocol}
                  </span>
                </td>
                <td>{c.resolution || '—'}</td>
                <td>{c.video_codec || '—'}</td>
                <td>{c.container || '—'}</td>
                <td>{formatSize(c.size_bytes)}</td>
                <td>{c.seeders}</td>
                <td>{c.score.toFixed(1)}</td>
                <td>
                  <button
                    className="play"
                    disabled={playingIndex !== null}
                    onClick={() => onPlay(i)}
                  >
                    {playingIndex === i ? 'Starting…' : 'Play'}
                  </button>
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      )}
    </div>
  )
}
