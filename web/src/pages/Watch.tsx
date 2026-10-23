import { useEffect, useRef, useState } from 'react'
import { useLocation, useNavigate } from 'react-router-dom'
import { getProgress } from '../api.ts'

interface WatchState {
  stream_url?: string
  subtitle_url?: string
  session_id?: string
}

export default function Watch() {
  const location = useLocation()
  const navigate = useNavigate()
  const state = (location.state ?? {}) as WatchState

  const { stream_url, subtitle_url, session_id } = state
  const [progress, setProgress] = useState<number | null>(null)
  const timerRef = useRef<ReturnType<typeof setInterval> | null>(null)

  useEffect(() => {
    if (!session_id) return
    let cancelled = false

    async function poll() {
      try {
        const p = await getProgress(session_id!)
        if (!cancelled) setProgress(p)
      } catch {
        // ignore transient polling errors
      }
    }

    poll()
    timerRef.current = setInterval(poll, 2000)
    return () => {
      cancelled = true
      if (timerRef.current) clearInterval(timerRef.current)
    }
  }, [session_id])

  if (!stream_url) {
    return (
      <div className="watch">
        <p className="empty">No stream selected.</p>
        <button className="back" onClick={() => navigate('/')}>
          ← Back to search
        </button>
      </div>
    )
  }

  const buffering = progress !== null && progress < 1
  const pct = progress !== null ? Math.round(progress * 100) : 0

  return (
    <div className="watch">
      <button className="back" onClick={() => navigate(-1)}>
        ← Back
      </button>
      <div className="player">
        <video controls autoPlay src={stream_url}>
          {subtitle_url && (
            <track
              kind="subtitles"
              src={subtitle_url}
              srcLang="en"
              label="English"
              default
            />
          )}
        </video>
      </div>
      {buffering && <div className="buffering">buffering {pct}%</div>}
    </div>
  )
}
