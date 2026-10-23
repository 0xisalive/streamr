import { useState } from 'react'
import type { FormEvent } from 'react'
import { useNavigate } from 'react-router-dom'
import { search } from '../api.ts'
import type { Result } from '../api.ts'

export default function Search() {
  const [query, setQuery] = useState('')
  const [results, setResults] = useState<Result[]>([])
  const [loading, setLoading] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const [searched, setSearched] = useState(false)
  const navigate = useNavigate()

  async function onSubmit(e: FormEvent) {
    e.preventDefault()
    const q = query.trim()
    if (!q) return
    setLoading(true)
    setError(null)
    try {
      const res = await search(q)
      setResults(res)
      setSearched(true)
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Search failed')
    } finally {
      setLoading(false)
    }
  }

  return (
    <div className="search">
      <form className="search-bar" onSubmit={onSubmit}>
        <input
          type="search"
          placeholder="Search movies & TV…"
          value={query}
          onChange={(e) => setQuery(e.target.value)}
          aria-label="Search"
        />
        <button type="submit" disabled={loading}>
          {loading ? 'Searching…' : 'Search'}
        </button>
      </form>

      {error && <p className="error">{error}</p>}

      {!error && searched && results.length === 0 && !loading && (
        <p className="empty">No results.</p>
      )}

      <div className="grid">
        {results.map((r) => (
          <button
            key={`${r.type}-${r.tmdb_id}`}
            className="card"
            onClick={() => navigate(`/title/${r.type}/${r.tmdb_id}`)}
          >
            <div className="poster">
              {r.poster_url ? (
                <img src={r.poster_url} alt={r.title} loading="lazy" />
              ) : (
                <div className="poster-fallback">{r.title}</div>
              )}
              <span className="rating">★ {r.rating.toFixed(1)}</span>
            </div>
            <div className="card-meta">
              <span className="card-title">{r.title}</span>
              <span className="card-year">{r.year || '—'}</span>
            </div>
          </button>
        ))}
      </div>
    </div>
  )
}
