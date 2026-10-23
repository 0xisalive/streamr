// Typed fetch wrapper for the streamr Go backend (base path /api).

export type MediaType = 'movie' | 'tv'
export type Protocol = 'torrent' | 'usenet' | 'ipfs'

export interface Result {
  tmdb_id: number
  type: MediaType
  title: string
  year: number
  overview: string
  poster_url: string
  rating: number
}

export interface Candidate {
  source: string
  protocol: Protocol
  title: string
  seeders: number
  size_bytes: number
  resolution: string
  video_codec: string
  container: string
  score: number
}

export interface SearchResponse {
  results: Result[]
}

export interface SourcesResponse {
  candidates: Candidate[]
}

export interface SourcesParams {
  tmdb_id: number
  type: MediaType
  season?: number
  episode?: number
}

export interface PlayBody {
  candidate_index: number
  tmdb_id: number
  type: string
  season: number
  episode: number
}

export interface PlayResponse {
  session_id: string
  stream_url: string
  subtitle_url: string
}

export interface ProgressResponse {
  progress: number
}

async function getJSON<T>(url: string): Promise<T> {
  const res = await fetch(url)
  if (!res.ok) {
    throw new Error(`Request failed (${res.status}): ${res.statusText}`)
  }
  return (await res.json()) as T
}

async function postJSON<T>(url: string, body: unknown): Promise<T> {
  const res = await fetch(url, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(body),
  })
  if (!res.ok) {
    throw new Error(`Request failed (${res.status}): ${res.statusText}`)
  }
  return (await res.json()) as T
}

export async function search(q: string): Promise<Result[]> {
  const data = await getJSON<SearchResponse>(
    `/api/search?q=${encodeURIComponent(q)}`,
  )
  return data.results ?? []
}

export async function getSources(params: SourcesParams): Promise<Candidate[]> {
  const qs = new URLSearchParams()
  qs.set('tmdb_id', String(params.tmdb_id))
  qs.set('type', params.type)
  if (params.season !== undefined) qs.set('season', String(params.season))
  if (params.episode !== undefined) qs.set('episode', String(params.episode))
  const data = await getJSON<SourcesResponse>(`/api/sources?${qs.toString()}`)
  return data.candidates ?? []
}

export async function play(body: PlayBody): Promise<PlayResponse> {
  return postJSON<PlayResponse>('/api/play', body)
}

export function streamUrl(sessionId: string): string {
  return `/api/stream/${encodeURIComponent(sessionId)}`
}

export function progressUrl(sessionId: string): string {
  return `/api/progress/${encodeURIComponent(sessionId)}`
}

export async function getProgress(sessionId: string): Promise<number> {
  const data = await getJSON<ProgressResponse>(progressUrl(sessionId))
  return data.progress
}
