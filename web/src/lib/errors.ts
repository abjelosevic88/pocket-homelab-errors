// Words, icons and small helpers shared by the pages.
import type { AppStateName, Level, Platform, Status } from './types'

export const LEVELS: Record<Level, { label: string; icon: string }> = {
  fatal: { label: 'Fatal', icon: 'zap' },
  error: { label: 'Error', icon: 'alert' },
  warning: { label: 'Warning', icon: 'flag' },
  info: { label: 'Info', icon: 'info' },
  debug: { label: 'Debug', icon: 'code' },
}

export const STATUS: Record<Status, { label: string; icon: string }> = {
  open: { label: 'Open', icon: 'circle' },
  resolved: { label: 'Resolved', icon: 'check-circle' },
  ignored: { label: 'Ignored', icon: 'eye-off' },
}

export const PLATFORMS: { value: Platform; label: string; icon: string; sub: string }[] = [
  { value: 'javascript', label: 'Website (JavaScript)', icon: 'globe', sub: 'Errors in visitors’ browsers' },
  { value: 'go', label: 'Go', icon: 'server', sub: 'A Go server or tool' },
  { value: 'python', label: 'Python', icon: 'code', sub: 'A script, a Django or Flask app' },
  { value: 'node', label: 'Node.js', icon: 'server', sub: 'A Node server or tool' },
  { value: 'docker', label: 'A container on this box', icon: 'package', sub: 'Read from its logs' },
  { value: 'other', label: 'Something else', icon: 'code', sub: 'Any Sentry SDK, or plain JSON' },
]
export const platformOf = (p: string) => PLATFORMS.find(x => x.value === p) || PLATFORMS[PLATFORMS.length - 1]

export const APP_INFO: Record<string, { name: string; icon: string }> = {
  alerts: { name: 'Pocket Alerts', icon: 'bell' },
  projects: { name: 'Pocket Projects', icon: 'check-square' },
  tasks: { name: 'Pocket Tasks', icon: 'check-circle' },
}

export const STATE_WORDS: Record<AppStateName, string> = {
  on: 'on', partly: 'partly on', off: 'off', link_off: 'link off', missing: 'not on this box', old: 'needs an update', no_core: 'no Pocket Core', not_set_up: 'not set up', error: 'not reachable',
}

/** "TypeError: x is undefined" → ["TypeError", "x is undefined"]. */
export function splitTitle(title: string, type: string): [string, string] {
  if (type && title.startsWith(type + ': ')) return [type, title.slice(type.length + 2)]
  if (type && title === type) return [type, '']
  return ['', title]
}

/** The short label of a container project's icon: its Pocket app's, else a box. */
export function projectIcon(p: { platform: string; app_slug?: string; source?: string }): string {
  if (p.source && p.source !== 'docker') return 'link'
  return platformOf(p.platform).icon
}

export const IGNORE_FOR: { value: string; label: string }[] = [
  { value: '1h', label: 'For an hour' }, { value: '24h', label: 'For a day' }, { value: '7d', label: 'For a week' }, { value: '30d', label: 'For a month' }, { value: '', label: 'For good' },
]

/** Hours (UTC buckets from the server) as "13:00" labels for the last 24. */
export function hourLabels(n = 24): string[] {
  const now = new Date()
  now.setMinutes(0, 0, 0)
  const out: string[] = []
  for (let i = n - 1; i >= 0; i--) {
    const t = new Date(now.getTime() - i * 3600_000)
    out.push(`${String(t.getHours()).padStart(2, '0')}:00`)
  }
  return out
}

export function sum(a?: number[]): number { return (a || []).reduce((s, n) => s + n, 0) }

/** A big number short: 1.2k. */
export function short(n: number): string {
  if (n < 1000) return String(n)
  if (n < 10000) return (n / 1000).toFixed(1).replace(/\.0$/, '') + 'k'
  if (n < 1e6) return Math.round(n / 1000) + 'k'
  return (n / 1e6).toFixed(1).replace(/\.0$/, '') + 'M'
}
