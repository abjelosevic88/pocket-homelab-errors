// The box's log lines: types and small helpers for the Logs page.
import { api, V1 } from './api'

export type LogLevel = 'debug' | 'info' | 'warning' | 'error' | 'fatal'
export type LogGroup = 'pocket' | 'docker' | 'system' | 'user' | 'kernel'
export type Keep = 'all' | 'warn' | 'error' | 'count'

export interface LogLine {
  id: number
  at: number
  source_id: number
  source: string
  source_label: string
  group: LogGroup
  level: LogLevel
  stream?: string
  pattern: string
  msg: string
  attrs?: Record<string, string>
}

export interface LogSource {
  id: number
  kind: 'docker' | 'journal'
  name: string
  label: string
  group: LogGroup
  image?: string
  keep: Keep
  first_at: number
  last_at: number
}

export interface SourceCount extends LogSource {
  lines: number
  errors: number
  warnings: number
  bytes: number
  spark: number[]
}

export interface Histogram { from: number; to: number; bucket: number; levels: Record<LogLevel, number[]>; total: number }

export interface Overview {
  volume: Histogram
  totals: Partial<Record<LogLevel | 'all', number>>
  previous: Partial<Record<LogLevel | 'all', number>>
  bytes: number
  sources: SourceCount[]
  heat: { sources: number[]; labels: string[]; bucket: number; from: number; lines: number[][]; errors: number[][] }
  groups: Partial<Record<LogGroup, number[]>>
}

export interface Pattern {
  id: string
  source_id: number
  source_label: string
  level: LogLevel
  template: string
  sample: string
  n: number
  total: number
  first_at: number
  last_at: number
  new: boolean
  share: number
  spark: number[]
}

export interface WebSite { host: string; n: number; ok: number; redirects: number; client_errors: number; server_errors: number; slow: number; avg_ms: number; spark: number[]; error_spark: number[] }
export interface Web { from: number; bucket: number; classes: Record<'2xx' | '3xx' | '4xx' | '5xx', number[]>; ms: number[]; sites: WebSite[]; total: number; errors: number; client: number; slow: number; avg_ms: number }

export interface LogSettings { on: boolean; pattern: string; journal: boolean; keep_days: number; max_mb: number }
export interface LogStatus {
  settings: LogSettings
  docker: { available: boolean; error: string; containers: number; lines: number; checked_at?: string }
  journal: { available: boolean; running: boolean; error: string; last_at: string; lines: number; dir: string }
  storage: { used: number; file: number; max: number; lines: number; oldest: number; newest: number }
  sources: number
}

export const logsApi = {
  status: () => api.get<LogStatus>(`${V1}/logs/status`),
  saveSettings: (s: Partial<LogSettings>) => api.put<LogSettings>(`${V1}/logs/settings`, s),
  clear: () => api.post(`${V1}/logs/clear`),
  lines: (q: string) => api.get<{ lines: LogLine[]; more: boolean }>(`${V1}/logs?${q}`),
  histogram: (q: string) => api.get<Histogram>(`${V1}/logs/histogram?${q}`),
  context: (id: number, n = 25) => api.get<{ lines: LogLine[]; id: number }>(`${V1}/logs/lines/${id}/context?n=${n}`),
  overview: (q: string) => api.get<Overview>(`${V1}/logs/overview?${q}`),
  patterns: (q: string) => api.get<{ patterns: Pattern[]; lines: number }>(`${V1}/logs/patterns?${q}`),
  web: (q: string) => api.get<Web>(`${V1}/logs/web?${q}`),
  sources: (q: string) => api.get<{ sources: SourceCount[]; bucket: number; from: number; total: number }>(`${V1}/logs/sources?${q}`),
  setKeep: (id: number, keep: Keep) => api.put<LogSource>(`${V1}/logs/sources/${id}`, { keep }),
}

// levels, worst first — status colours: never alone, always with the word
export const LOG_LEVELS: { id: LogLevel; label: string; short: string }[] = [
  { id: 'fatal', label: 'Fatal', short: 'FTL' },
  { id: 'error', label: 'Error', short: 'ERR' },
  { id: 'warning', label: 'Warning', short: 'WRN' },
  { id: 'info', label: 'Info', short: 'INF' },
  { id: 'debug', label: 'Debug', short: 'DBG' },
]

// the stack order of the volume bars: the bad ones sit on the baseline, where they are easiest to compare
export const STACK: LogLevel[] = ['fatal', 'error', 'warning', 'info', 'debug']

// source groups — categorical slots 1–5 in fixed order (validated: adjacent CVD ΔE ≥ 9.1 light, ≥ 8.4 dark)
export const GROUPS: { id: LogGroup; label: string; icon: string; sub: string }[] = [
  { id: 'pocket', label: 'Pocket apps', icon: 'layers', sub: 'Containers named pocket-*' },
  { id: 'docker', label: 'Other containers', icon: 'package', sub: 'Everything else in Docker' },
  { id: 'system', label: 'System services', icon: 'server', sub: 'systemd units, from the journal' },
  { id: 'user', label: 'Your services', icon: 'user', sub: 'Your user’s systemd units (Pocket AI…)' },
  { id: 'kernel', label: 'Kernel', icon: 'zap', sub: 'The kernel’s own messages' },
]
export const groupOf = (g: string) => GROUPS.find(x => x.id === g) || GROUPS[1]

export const KEEPS: { value: Keep; label: string; hint: string }[] = [
  { value: 'all', label: 'Every line', hint: 'Keep and search everything it writes' },
  { value: 'warn', label: 'Warnings and worse', hint: 'Count the rest, keep only what went wrong' },
  { value: 'error', label: 'Errors only', hint: 'Count the rest, keep only errors' },
  { value: 'count', label: 'Count only', hint: 'Keep no lines, only the counts and repeating messages' },
]

export const RANGES = [
  { value: '1h', label: '1h', ms: 3600e3 },
  { value: '6h', label: '6h', ms: 6 * 3600e3 },
  { value: '24h', label: '24h', ms: 24 * 3600e3 },
  { value: '7d', label: '7d', ms: 7 * 86400e3 },
  { value: '30d', label: '30d', ms: 30 * 86400e3 },
] as const
export type RangeId = typeof RANGES[number]['value']

export function fmtCount(n: number): string {
  if (n >= 1e9) return (n / 1e9).toFixed(n >= 1e10 ? 0 : 1).replace(/\.0$/, '') + 'B'
  if (n >= 1e6) return (n / 1e6).toFixed(n >= 1e7 ? 0 : 1).replace(/\.0$/, '') + 'M'
  if (n >= 1e4) return Math.round(n / 1e3) + 'k'
  if (n >= 1e3) return (n / 1e3).toFixed(1).replace(/\.0$/, '') + 'k'
  return String(n)
}

export function fmtBytes(n: number): string {
  if (n >= 1 << 30) return (n / (1 << 30)).toFixed(1) + ' GB'
  if (n >= 1 << 20) return (n / (1 << 20)).toFixed(n >= 100 << 20 ? 0 : 1) + ' MB'
  if (n >= 1 << 10) return Math.round(n / 1024) + ' KB'
  return n + ' B'
}

export function fmtMs(ms: number): string {
  if (ms >= 1000) return (ms / 1000).toFixed(ms >= 10000 ? 0 : 1) + ' s'
  return Math.round(ms) + ' ms'
}

/** Change against the span before: "+12%" / "−3%" / "new". */
export function delta(now: number, before: number): { text: string; dir: 'up' | 'down' | 'flat' } {
  if (!before) return { text: now ? 'new' : '—', dir: now ? 'up' : 'flat' }
  const p = (now - before) / before * 100
  if (Math.abs(p) < 1) return { text: '±0%', dir: 'flat' }
  return { text: (p > 0 ? '+' : '−') + Math.abs(p).toFixed(Math.abs(p) >= 10 ? 0 : 1) + '%', dir: p > 0 ? 'up' : 'down' }
}

const pad = (n: number) => String(n).padStart(2, '0')

/** A bar's start, as short as the bar width allows. */
export function barLabel(at: number, bucket: number, long = false): string {
  const d = new Date(at)
  const hm = `${pad(d.getHours())}:${pad(d.getMinutes())}`
  const day = d.toLocaleDateString(undefined, { weekday: 'short', day: 'numeric', month: 'short' })
  if (bucket >= 86400e3) return day
  if (bucket >= 3600e3) return long ? `${day} ${hm}` : (d.getHours() === 0 ? d.toLocaleDateString(undefined, { day: 'numeric', month: 'short' }) : hm)
  return long ? `${day} ${hm}` : hm
}

/** A line's time: the clock with seconds, the day when it is not today. */
export function lineTime(at: number): { clock: string; day: string } {
  const d = new Date(at)
  const clock = `${pad(d.getHours())}:${pad(d.getMinutes())}:${pad(d.getSeconds())}.${String(d.getMilliseconds()).padStart(3, '0')}`
  const today = new Date()
  const same = d.toDateString() === today.toDateString()
  return { clock, day: same ? '' : d.toLocaleDateString(undefined, { day: 'numeric', month: 'short' }) }
}

export function sumLevels(h: Histogram | undefined, i: number): number {
  if (!h) return 0
  return STACK.reduce((s, l) => s + (h.levels[l]?.[i] || 0), 0)
}

/** Words of a search to mark in a line. */
export function searchTerms(q: string): string[] {
  return q.split(/\s+/).filter(w => w && !w.startsWith('-')).map(w => w.replace(/^"|"$/g, '')).filter(w => w.length >= 2)
}
