import { useEffect, useRef, useState } from 'react'
import { useLocation, useNavigate } from 'react-router-dom'
import Hls from 'hls.js'
import { getProgress } from '../api.ts'

interface WatchState {
  stream_url?: string
  subtitle_url?: string
  session_id?: string
  delivery?: string // "direct" | "remux" | "transcode"
}

export default function Watch() {
  const location = useLocation()
  const navigate = useNavigate()
  const state = (location.state ?? {}) as WatchState

  const { stream_url, subtitle_url, session_id, delivery } = state
  const [progress, setProgress] = useState<number | null>(null)
  const [subtitleReady, setSubtitleReady] = useState(false)
  const timerRef = useRef<ReturnType<typeof setInterval> | null>(null)
  const videoRef = useRef<HTMLVideoElement | null>(null)

  // For transcode (HLS) delivery, attach hls.js (or use native HLS on Safari).
  useEffect(() => {
    const video = videoRef.current
    if (!video || !stream_url) return
    const isHls = delivery === 'transcode' || stream_url.endsWith('.m3u8')
    if (!isHls) return

    if (video.canPlayType('application/vnd.apple.mpegurl')) {
      video.src = stream_url // native HLS (Safari)
      return
    }
    if (Hls.isSupported()) {
      const hls = new Hls({ enableWorker: true, lowLatencyMode: false })
      hls.loadSource(stream_url)
      hls.attachMedia(video)
      return () => hls.destroy()
    }
  }, [stream_url, delivery])

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

  // Subtitles are produced asynchronously (fetch+align or whisper generation).
  // Poll the endpoint: 202 = still working, 200 = ready. Only then inject the
  // <track>, so a single early fetch doesn't permanently fail (the browser
  // would otherwise never retry).
  useEffect(() => {
    if (!subtitle_url) return
    let cancelled = false
    const timer = setInterval(async () => {
      try {
        // 200 = ready; 202 = still working (keep polling). Check status
        // explicitly since 202 is also "ok" (2xx).
        const res = await fetch(subtitle_url, { method: 'HEAD' })
        if (!cancelled && res.status === 200) {
          setSubtitleReady(true)
          clearInterval(timer)
        }
      } catch {
        // keep polling
      }
    }, 3000)
    return () => {
      cancelled = true
      clearInterval(timer)
    }
  }, [subtitle_url])

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
  const isHls = delivery === 'transcode' || stream_url.endsWith('.m3u8')

  return (
    <div className="watch">
      <button className="back" onClick={() => navigate(-1)}>
        ← Back
      </button>
      <div className="player">
        {/* For HLS, hls.js attaches the source in the effect above; for
            direct/remux delivery we point src straight at the stream. */}
        <video ref={videoRef} controls autoPlay src={isHls ? undefined : stream_url}>
          {subtitle_url && subtitleReady && (
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
      <div className="watch-meta">
        {delivery && <span className={`badge badge-${delivery}`}>{delivery}</span>}
        {buffering && <span className="buffering">buffering {pct}%</span>}
        {subtitle_url && !subtitleReady && (
          <span className="buffering">preparing subtitles…</span>
        )}
      </div>
    </div>
  )
}
