// The person's summary, projects and app states, shared by every page.
import React, { createContext, useCallback, useContext, useEffect, useState } from 'react'
import { api, V1 } from './api'
import type { AppState, Project, Summary } from './types'

const EVENT = 'per:changed'

interface DataState {
  summary: Summary | null
  projects: Project[] | null
  apps: AppState[] | null
  reload: () => Promise<void>
  reloadApps: () => Promise<void>
}
const Ctx = createContext<DataState>({ summary: null, projects: null, apps: null, reload: async () => {}, reloadApps: async () => {} })
export function useData() { return useContext(Ctx) }

/** Something changed (an issue resolved, a project added): lists and counts reload. */
export function announceChange() { window.dispatchEvent(new CustomEvent(EVENT)) }

export function useOnChange(fn: () => void) {
  useEffect(() => {
    window.addEventListener(EVENT, fn)
    return () => window.removeEventListener(EVENT, fn)
  }, [fn])
}

const emptySummary: Summary = {
  overview: { open: 0, new: 0, regressed: 0, events_24h: 0, projects: 0, quiet_projects: 0, hours: { fatal: [], error: [], warning: [], info: [] } },
  latest: [], watch: { on: false, checked_at: '', error: '', containers: [] },
}

export function DataProvider({ children }: { children: React.ReactNode }) {
  const [summary, setSummary] = useState<Summary | null>(null)
  const [projects, setProjects] = useState<Project[] | null>(null)
  const [apps, setApps] = useState<AppState[] | null>(null)
  const reload = useCallback(async () => {
    const [sum, pr] = await Promise.all([
      api.get<Summary>(`${V1}/summary`).catch(() => null),
      api.get<{ projects: Project[] }>(`${V1}/projects`).then(r => r.projects).catch(() => null),
    ])
    setSummary(s => sum || s || emptySummary)
    setProjects(p => pr || p || [])
  }, [])
  const reloadApps = useCallback(async () => {
    const a = await api.get<{ apps: AppState[] }>(`${V1}/apps`).then(r => r.apps).catch(() => null)
    setApps(x => a || x || [])
  }, [])
  useEffect(() => { reload(); reloadApps() }, [reload, reloadApps])
  useOnChange(reload)
  // errors keep coming in: a quiet refresh every half minute while the page is seen
  useEffect(() => {
    const t = window.setInterval(() => { if (document.visibilityState === 'visible') reload() }, 30000)
    const a = window.setInterval(() => { if (document.visibilityState === 'visible') reloadApps() }, 120000)
    return () => { window.clearInterval(t); window.clearInterval(a) }
  }, [reload, reloadApps])
  return <Ctx.Provider value={{ summary, projects, apps, reload, reloadApps }}>{children}</Ctx.Provider>
}

/** One app's state for this person; `ok` = the scope works. */
export function useAppState() {
  const { apps } = useData()
  return (slug: string, scope?: string) => {
    const a = apps?.find(x => x.slug === slug)
    const ok = !!a && a.via !== '' && (!scope || a.on.includes(scope))
    return { app: a, ok, loading: !apps }
  }
}
