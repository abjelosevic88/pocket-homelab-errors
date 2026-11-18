// Inside Pocket Hub: what needs you (open issues that are new today or came back) for the
// hub's Activity panel, its sidebar badge and the Dock. Does nothing outside the hub.
import { useCallback, useEffect, useRef, useState } from 'react'
import { useNavigate } from 'react-router-dom'
import { api, V1 } from '../lib/api'
import { useApp } from '../lib/app-context'
import { useOnChange } from '../lib/data'
import { ago } from '../lib/format'
import type { Issue, Summary } from '../lib/types'

type PocketApp = { inHub?: boolean; report: (r: Record<string, unknown>) => void }
const pocketApp = () => (window as unknown as { pocketApp?: PocketApp }).pocketApp

const LOOK = 100 // the most recently seen open issues looked at
const ITEMS = 15 // listed in Activity
const EVERY = 60_000 // on screen
const HIDDEN = 120_000 // kept ready behind the app on screen: Activity is for these

const item = (is: Issue) => ({
  id: String(is.id),
  title: is.title || is.type || 'Error',
  detail: [is.regressed ? 'Came back' : 'New', is.project_name, `${is.times.toLocaleString()} event${is.times === 1 ? '' : 's'}`, `seen ${ago(is.last_seen)}`].join(' · '),
  state: 'failed',
  url: `${location.origin}/issues/${is.id}`,
  at: is.last_seen,
})

export default function HubReport() {
  const { user } = useApp()
  const navigate = useNavigate()
  // pocket-ask.js is deferred: pocketApp may come after the first render
  const [inHub, setInHub] = useState(() => !!pocketApp()?.inHub)
  useEffect(() => {
    if (inHub) return
    const ready = () => setInHub(!!pocketApp()?.inHub)
    window.addEventListener('pocket-app-ready', ready)
    ready()
    return () => window.removeEventListener('pocket-app-ready', ready)
  }, [inHub])

  const sent = useRef('')
  const seq = useRef(0)
  const askedAt = useRef(0)
  const send = useCallback((r: { count: number; items: ReturnType<typeof item>[] }) => {
    const key = JSON.stringify(r)
    if (key === sent.current) return
    sent.current = key
    pocketApp()?.report({ ...r, status: null, open: (id: string) => navigate(`/issues/${id}`) })
  }, [navigate])

  const refresh = useCallback(async () => {
    if (!inHub || !user) return
    const n = ++seq.current
    askedAt.current = Date.now()
    try {
      const r = await api.get<{ issues: Issue[]; total: number }>(`${V1}/issues?status=open&sort=last&limit=${LOOK}`)
      const needs = r.issues.filter(is => is.new || is.regressed)
      let count = needs.length
      if (r.total > r.issues.length) {
        // more open issues than looked at: the counts are the server's (new and came back can overlap)
        const o = (await api.get<Summary>(`${V1}/summary`)).overview
        count = Math.max(count, o.new + o.regressed - needs.filter(is => is.new && is.regressed).length)
      }
      if (n === seq.current) send({ count, items: needs.slice(0, ITEMS).map(item) })
    } catch { /* keep what the hub has */ }
  }, [inHub, user, send])

  useEffect(() => {
    if (!inHub || !user) return
    refresh()
    const t = window.setInterval(() => {
      const every = document.visibilityState === 'visible' ? EVERY : HIDDEN
      if (Date.now() - askedAt.current >= every - 5000) refresh()
    }, EVERY)
    const seen = () => { if (document.visibilityState === 'visible') refresh() }
    document.addEventListener('visibilitychange', seen)
    return () => { window.clearInterval(t); document.removeEventListener('visibilitychange', seen) }
  }, [inHub, user, refresh])
  useOnChange(refresh) // an issue resolved or ignored here

  // signed out: nothing is waiting any more
  useEffect(() => {
    if (user || !sent.current) return
    sent.current = ''
    pocketApp()?.report({ count: 0, status: null, items: [] })
  }, [user])

  return null
}
