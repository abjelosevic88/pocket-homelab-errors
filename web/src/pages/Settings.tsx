import { AskButton } from '../components/AskButton'
import React, { useEffect, useState } from 'react'
import { useNavigate, useParams } from 'react-router-dom'
import { api, V1 } from '../lib/api'
import { useApp } from '../lib/app-context'
import type { APIToken, AppSlug, AppState, ConnMode, Settings as SettingsT, User } from '../lib/types'
import { DATE_FORMATS, TIME_FORMATS, ago, fmtBytes, fmtDateTime, plural, type DateFormat, type TimeFormat } from '../lib/format'
import { Avatar, Confirm, CoreHint, Field, Loading, Modal, Segmented, SettingRow, Switch, Tabs, useAsync, useIsMobile, useToast } from '../components/ui'
import { Icon } from '../components/icons'
import { Select } from '../components/Select'
import { ShortcutsSheet } from '../components/ShortcutsSheet'
import { PocketModelSelect } from '../components/PocketModelSelect'
import { APP_INFO, STATE_WORDS } from '../lib/errors'
import { useData } from '../lib/data'
import { upload } from '../lib/api'

type TabId = 'general' | 'notifications' | 'box' | 'apps' | 'ai' | 'profile' | 'people' | 'tokens' | 'data' | 'about'

const TABS: { id: TabId; label: string; icon: string; sub: string }[] = [
  { id: 'general', label: 'General', icon: 'home', sub: 'How long events are kept, what is never kept, dates and the app title.' },
  { id: 'notifications', label: 'Telling you', icon: 'bell', sub: 'How you hear about a new error, one that came back, or one happening a lot: Pocket Alerts, your own push, or both.' },
  { id: 'box', label: 'This box', icon: 'package', sub: 'Read the logs of the containers on this box for errors — every Pocket app included, nothing to install.' },
  { id: 'apps', label: 'Connected apps', icon: 'link', sub: 'Pocket Alerts, Projects and Tasks — and what the other apps may read here.' },
  { id: 'ai', label: 'AI', icon: 'sparkles', sub: 'Who explains errors: Pocket AI through Core, or your own OpenAI key.' },
  { id: 'profile', label: 'Profile', icon: 'user', sub: 'Your name, email and password.' },
  { id: 'people', label: 'People', icon: 'users', sub: 'Everyone who has an account on this box. Each person has their own projects, issues and connections.' },
  { id: 'tokens', label: 'API & shortcuts', icon: 'key', sub: 'Personal tokens for scripts that send errors, dashboards and other tools.' },
  { id: 'data', label: 'Backup & data', icon: 'database', sub: 'Export everything, bring an export back, download the database.' },
  { id: 'about', label: 'About', icon: 'info', sub: 'Version, numbers and the server.' },
]

export default function Settings() {
  const { tab: raw } = useParams()
  const navigate = useNavigate()
  const mobile = useIsMobile()
  const tab: TabId = TABS.find(t => t.id === raw)?.id || 'general'
  const current = TABS.find(t => t.id === tab)!
  const go = (id: TabId) => navigate(`/settings/${id}`, { replace: true })
  useEffect(() => { document.querySelector('.set-content')?.scrollTo({ top: 0 }) }, [tab])
  useEffect(() => {
    const h = (e: KeyboardEvent) => { if (e.key === 'Escape' && !document.querySelector('.modal-backdrop, .menu, .sel-pop')) navigate('/') }
    window.addEventListener('keydown', h)
    return () => window.removeEventListener('keydown', h)
  }, [navigate])
  return (
    <div className="set-page">
      <header className="set-header">
        <button type="button" className="btn ghost icon-only" aria-label="Back" title="Back (esc)" onClick={() => navigate('/')}><Icon name="chevron-left" size={18} /></button>
        <img src="/favicon.svg" alt="" width={24} height={24} className="set-logo" />
        <h1>Settings</h1>
        <span style={{ flex: 1 }} />
        <AskButton />
      </header>
      {mobile && <div className="set-tabs-mobile"><Tabs tabs={TABS.map(t => ({ id: t.id, label: t.label, icon: t.icon }))} value={tab} onChange={go} /></div>}
      <div className="set-body">
        {!mobile && (
          <nav className="set-nav" aria-label="Settings sections">
            {TABS.map(t => (
              <button key={t.id} type="button" className={`set-nav-item ${t.id === tab ? 'active' : ''}`} aria-current={t.id === tab ? 'page' : undefined} onClick={() => go(t.id)}>
                <Icon name={t.icon} size={16} />{t.label}
              </button>
            ))}
          </nav>
        )}
        <main className="set-content">
          <div className="set-inner" key={tab}>
            <div className="set-title"><h2>{current.label}</h2><p>{current.sub}</p></div>
            {tab === 'general' && <General />}
            {tab === 'notifications' && <Notifications />}
            {tab === 'box' && <Box />}
            {tab === 'apps' && <Integrations />}
            {tab === 'ai' && <AITab />}
            {tab === 'profile' && <Profile />}
            {tab === 'people' && <Users />}
            {tab === 'tokens' && <Tokens />}
            {tab === 'data' && <Data />}
            {tab === 'about' && <About />}
          </div>
        </main>
      </div>
    </div>
  )
}

function Section({ title, sub, icon, actions, children }: { title: React.ReactNode; sub?: React.ReactNode; icon?: string; actions?: React.ReactNode; children?: React.ReactNode }) {
  return (
    <section className="set-section">
      <div className="set-section-head">
        {icon && <span className="set-app-ico"><Icon name={icon} size={17} /></span>}
        <div className="grow"><h3>{title}</h3>{sub && <p>{sub}</p>}</div>
        {actions && <div className="row">{actions}</div>}
      </div>
      {children && <div className="set-section-body">{children}</div>}
    </section>
  )
}

function useSettingsForm() {
  const { settings, setSettings, user } = useApp()
  const { toast } = useToast()
  const [f, setF] = useState<SettingsT | null>(settings)
  const [busy, setBusy] = useState(false)
  useEffect(() => { if (!f && settings) setF(settings) }, [settings]) // eslint-disable-line react-hooks/exhaustive-deps
  const save = async (patch?: Partial<SettingsT>) => {
    if (!f) return
    setBusy(true)
    try { const s = await api.put<SettingsT>(`${V1}/settings`, { ...f, ...patch }); setSettings(s); setF(s); toast('Saved') } catch (e) { toast((e as Error).message, 'err') } finally { setBusy(false) }
  }
  return { f, setF, save, busy, isAdmin: user?.role === 'admin' }
}

function useTester() {
  const [test, setTest] = useState<Record<string, string>>({})
  const run = async (which: string, url: string, key: string, extra: Record<string, string> = {}) => {
    setTest(t => ({ ...t, [which]: 'testing…' }))
    try { const r = await api.post<{ ok: boolean; message?: string; error?: string }>(`${V1}/integrations/test`, { which, url, key, ...extra }); setTest(t => ({ ...t, [which]: r.ok ? `✓ ${r.message}` : `✕ ${r.error}` })) } catch (e) { setTest(t => ({ ...t, [which]: `✕ ${(e as Error).message}` })) }
  }
  const status = (which: string) => <span className={`small ${test[which]?.startsWith('✓') ? 'ok' : test[which]?.startsWith('✕') ? 'bad' : 'muted'}`}>{test[which]}</span>
  return { run, status }
}

function General() {
  const { f, setF, save, busy, isAdmin } = useSettingsForm()
  if (!f) return <Loading />
  return (
    <div className="stack loose">
      <Section title="Keeping" icon="archive">
        <SettingRow title="Keep events for" help="Each time an error happened is kept this many days, with its stack and what came before it (at most the newest 200 of an issue). Issues and their counts stay; closed issues not seen for three times as long are tidied away.">
          <Select ariaLabel="Keep events for" value={String(f.keep_days)} onChange={v => setF({ ...f, keep_days: Number(v) })} options={[7, 14, 30, 60, 90, 180, 365].map(d => ({ value: String(d), label: plural(d, 'day') }))} />
        </SettingRow>
        <SettingRow title="Leave out people’s addresses" help="The IP addresses of your apps’ users are dropped before anything is kept (they are still counted as people, without the address). Passwords, tokens, cookies and keys are never kept either way."><Switch checked={f.scrub_ips} onChange={v => setF({ ...f, scrub_ips: v })} label="Leave out addresses" /></SettingRow>
      </Section>
      <Section title="Dates" icon="calendar">
        <SettingRow title="Week starts on"><Segmented size="sm" value={f.week_start} onChange={v => setF({ ...f, week_start: v })} options={[{ value: 1, label: 'Monday' }, { value: 6, label: 'Saturday' }, { value: 7, label: 'Sunday' }]} /></SettingRow>
        <SettingRow title="Date format"><Select ariaLabel="Date format" value={f.date_format} onChange={v => setF({ ...f, date_format: v as DateFormat })} options={DATE_FORMATS.map(d => ({ value: d.id, label: d.label }))} /></SettingRow>
        <SettingRow title="Time format"><Select ariaLabel="Time format" value={f.time_format} onChange={v => setF({ ...f, time_format: v as TimeFormat })} options={TIME_FORMATS.map(d => ({ value: d.id, label: d.label }))} /></SettingRow>
        {isAdmin && <SettingRow title="App title" help="Shown in the sidebar for everyone on this box."><input value={f.title} onChange={e => setF({ ...f, title: e.target.value })} /></SettingRow>}
      </Section>
      <div className="row"><button type="button" className="btn primary" onClick={() => save()} disabled={busy}>Save changes</button></div>
    </div>
  )
}

function Notifications() {
  const { f, setF, save, busy } = useSettingsForm()
  const { run, status } = useTester()
  const { data: states } = useAsync(() => api.get<{ apps: AppState[] }>(`${V1}/apps`).then(r => r.apps).catch(() => [] as AppState[]), [f?.alerts_mode])
  if (!f) return <Loading />
  const coreOn = f.core_configured && f.core_registered !== false
  const alerts = states?.find(x => x.slug === 'alerts')
  const alertsOn = alerts?.state === 'on'
  return (
    <div className="stack loose">
      <Section title="How you are told" icon="bell" sub="Which errors you hear about is set per project (a new one, one that came back, one happening a lot). At most 30 a hour reach you, so a storm of errors is one look at the inbox, not a buzzing phone.">
        <SettingRow title="Through" help={alertsOn ? 'Pocket Alerts is connected: your own rules and quiet hours apply there.' : coreOn ? `Pocket Alerts: ${alerts ? STATE_WORDS[alerts.state] : 'checking…'}${alerts?.fix ? ` — ${alerts.fix}` : ''}.` : undefined}>
          <Select ariaLabel="Through" value={!coreOn && (f.notify_via === 'alerts' || f.notify_via === 'both') ? 'push' : f.notify_via} onChange={v => setF({ ...f, notify_via: v as SettingsT['notify_via'] })} options={[
            { value: 'auto', label: 'Pocket Alerts when it is there, else my push' },
            { value: 'alerts', label: 'Pocket Alerts', disabled: !coreOn, hint: coreOn ? undefined : 'needs Pocket Core' },
            { value: 'push', label: 'My own push (ntfy, Apprise…)' },
            { value: 'both', label: 'Both', disabled: !coreOn, hint: coreOn ? undefined : 'needs Pocket Core' },
            { value: 'off', label: 'Not at all' },
          ]} />
        </SettingRow>
        {!coreOn && <CoreHint />}
      </Section>
      <Section title="Your own push" icon="smartphone" sub="An ntfy topic, an Apprise address or any URL that takes JSON.">
        <div className="two-col tight">
          <Field label="Address"><input value={f.notify_url} onChange={e => setF({ ...f, notify_url: e.target.value })} placeholder="https://ntfy.sh/my-errors" autoComplete="off" /></Field>
          <Field group label="Kind"><Select ariaLabel="Kind" value={f.notify_format || ''} onChange={v => setF({ ...f, notify_format: v })} options={[{ value: '', label: 'Guess from the address' }, { value: 'ntfy', label: 'ntfy' }, { value: 'apprise', label: 'Apprise' }, { value: 'json', label: 'JSON' }]} /></Field>
        </div>
        <div className="row wrap"><button type="button" className="btn sm" onClick={() => run('notify', f.notify_url, '', { format: f.notify_format })} disabled={!f.notify_url}>Send a test</button>{status('notify')}</div>
      </Section>
      <div className="row"><button type="button" className="btn primary" onClick={() => save()} disabled={busy}>Save changes</button></div>
    </div>
  )
}

interface WatchInfo { available: boolean; admin: boolean; on: boolean; pattern: string; error?: string; status: { on: boolean; checked_at: string; error: string; containers: { name: string; image: string; project_id: number; archived: boolean; errors: number; last_error_at: string }[] }; containers?: { name: string; image: string; match: boolean }[] }

function Box() {
  const { f, setF, save, busy } = useSettingsForm()
  const { reload } = useData()
  const { data: w, reload: again } = useAsync(() => api.get<WatchInfo>(`${V1}/watch?all=1`), [f?.watch_docker, f?.watch_pattern])
  if (!f || !w) return <Loading />
  const can = w.admin && w.available
  const matching = (w.containers || []).filter(c => c.match)
  const read = new Map(w.status.containers.map(c => [c.name, c]))
  return (
    <div className="stack loose">
      <Section title="Read this box’s containers" icon="package" sub="Every 15 seconds Errors reads what the containers wrote and keeps their error lines, Go panics and Python tracebacks — so the server errors of every Pocket app arrive with nothing to change in them. Only what is written after you switch it on is read. Each container gets its own project with its first error, named as Pocket Core knows it.">
        {!w.admin && <div className="callout info"><Icon name="lock" size={15} /><div>Only an admin can switch this on: the containers belong to the whole box.</div></div>}
        {w.admin && !w.available && <div className="callout warn"><Icon name="alert" size={15} /><div>The Docker socket is not here. Mount it into this container — <code>/var/run/docker.sock:/var/run/docker.sock:ro</code> under <code>volumes</code> — and start it again. Errors only reads container lists and logs.</div></div>}
        <SettingRow title="Read the containers’ logs"><Switch checked={f.watch_docker} disabled={!can} onChange={v => save({ watch_docker: v })} label="Read the containers" /></SettingRow>
        <SettingRow title="Which ones" help="Container names, * for anything, commas between: pocket-* for every Pocket app, add caddy or others. Archive a container’s project to leave it out.">
          <div className="row"><input value={f.watch_pattern} disabled={!can} onChange={e => setF({ ...f, watch_pattern: e.target.value })} placeholder="pocket-*" /><button type="button" className="btn" disabled={!can || busy} onClick={() => save({ watch_pattern: f.watch_pattern })}>Save</button></div>
        </SettingRow>
      </Section>
      {can && <Section title={`${plural(matching.length, 'container')} match`} icon="list" actions={<button type="button" className="btn ghost sm" onClick={() => { again(); reload() }}><Icon name="refresh" size={13} />Look again</button>}
        sub={w.status.on ? (w.status.error ? undefined : `Last looked ${w.status.checked_at ? ago(w.status.checked_at) : '—'}.`) : f.watch_docker ? 'Starting: the first look is within 15 seconds.' : 'Switched off: these would be read.'}>
        {w.status.error && <div className="callout warn"><Icon name="alert" size={15} /><div>{w.status.error}</div></div>}
        {w.error && <div className="callout warn"><Icon name="alert" size={15} /><div>{w.error}</div></div>}
        <ul className="set-list watch-list">
          {matching.map(c => {
            const st = read.get(c.name)
            return (
              <li key={c.name}>
                <Icon name="package" size={14} />
                <span className="grow ellipsis"><b>{c.name}</b> <span className="muted small">{c.image}</span></span>
                {st?.archived ? <span className="badge">archived</span> : st?.errors ? <span className="badge warn">{st.errors} since start</span> : st?.project_id ? <span className="badge">quiet</span> : <span className="muted small">no errors yet</span>}
                {st?.project_id ? <a className="btn ghost sm" href={`/projects/${st.project_id}`}>Project</a> : null}
              </li>
            )
          })}
          {!matching.length && <li className="muted">No running container matches.</li>}
        </ul>
      </Section>}
    </div>
  )
}

const APPS: { slug: AppSlug; title: string; sub: string; token: string; port: string }[] = [
  { slug: 'alerts', title: 'Pocket Alerts — telling you', sub: 'New errors, ones that came back and ones happening a lot reach your phone through your alerts inbox, by your rules and quiet hours.', token: 'Pocket Alerts → Settings → API tokens', port: '8217' },
  { slug: 'projects', title: 'Pocket Projects — issues to fix', sub: 'Send to → Pocket Projects issue: what broke, where, how often, the stack and a link back, in the team you pick.', token: 'Pocket Projects → Settings → API tokens', port: '8196' },
  { slug: 'tasks', title: 'Pocket Tasks — a task to look into it', sub: 'Send to → Pocket Tasks task, with a link back to the issue.', token: 'Pocket Tasks → Settings → API tokens', port: '8184' },
]

function Integrations() {
  const { f, setF, save, busy } = useSettingsForm()
  const { run, status } = useTester()
  const { data: states } = useAsync(() => api.get<{ apps: AppState[] }>(`${V1}/apps`).then(r => r.apps).catch(() => [] as AppState[]), [f?.alerts_mode, f?.projects_mode, f?.tasks_mode])
  const projectsOn = states?.find(x => x.slug === 'projects')?.state === 'on'
  const { data: teams } = useAsync(() => projectsOn ? api.get<{ teams: { key: string; name: string }[] }>(`${V1}/projects-teams`).then(r => r.teams).catch(() => []) : Promise.resolve([]), [projectsOn])
  if (!f) return <Loading />
  const coreOn = f.core_configured && f.core_registered !== false
  const eff = (m: ConnMode): ConnMode => !coreOn && m === 'core' ? 'off' : m
  const set = (k: string, v: string) => setF({ ...f, [k]: v } as SettingsT)
  return (
    <div className="stack loose">
      <div className="callout info"><Icon name="info" size={15} /><div>These connections are yours. Errors keeps working without any of them. Through <b>Pocket Core</b> Errors acts as you in your own Pocket apps with nothing to paste (switch it off for yourself in Pocket Core → Settings → Apps acting for me); <b>Direct</b> uses a URL and an API token you enter.{!coreOn && <CoreHint />}</div></div>
      {APPS.map(a => {
        const mode = f[`${a.slug}_mode`]
        const via = f[`${a.slug}_via`]
        const name = APP_INFO[a.slug].name
        const st = states?.find(x => x.slug === a.slug)
        return (
          <Section key={a.slug} title={a.title} icon={APP_INFO[a.slug].icon} actions={st && st.state !== 'on' && st.state !== 'off' ? <span className={`badge ${st.state === 'partly' ? 'ok' : 'warn'}`}>{STATE_WORDS[st.state]}</span> : connBadge(eff(mode), via)} sub={a.sub}>
            <Connection app={name} mode={mode} via={via} detail={f[`${a.slug}_detail`]} core={coreOn} hasToken={!!f[`${a.slug}_token`]}
              onMode={m => save({ [`${a.slug}_mode`]: m } as Partial<SettingsT>)} onDisconnect={() => save({ [`${a.slug}_url`]: '', [`${a.slug}_token`]: '' } as Partial<SettingsT>)}
              test={<><button type="button" className="btn sm" onClick={() => run(a.slug, f[`${a.slug}_url`], f[`${a.slug}_token`])}>Test connection</button>{status(a.slug)}</>}
              direct={<div className="two-col tight">
                <Field label="URL" help="Inside Docker use http://host.docker.internal:PORT or the service name."><input value={f[`${a.slug}_url`]} onChange={e => set(`${a.slug}_url`, e.target.value)} placeholder={`http://host.docker.internal:${a.port}`} /></Field>
                <Field label="API token" help={a.token}><input value={f[`${a.slug}_token`]} onChange={e => set(`${a.slug}_token`, e.target.value)} autoComplete="off" /></Field>
              </div>} />
            {st?.fix && st.state !== 'on' && <p className="muted small"><Icon name="info" size={12} className="inline-ico" /> {st.fix}.</p>}
            {a.slug === 'projects' && <SettingRow title="Team" help="Where Send to → Pocket Projects puts the issue first (you can pick another each time).">
              <Select ariaLabel="Team" disabled={!projectsOn} value={f.projects_team} onChange={v => save({ projects_team: v })} options={[{ value: '', label: projectsOn ? 'Ask each time' : 'Connect Pocket Projects first' }, ...(teams || []).map(t => ({ value: t.key, label: t.name, hint: t.key }))]} />
            </SettingRow>}
          </Section>
        )
      })}
      <Section title="Other Pocket apps" icon="layers" sub="Errors offers your errors to the rest of the box through Pocket Core. Switched off until you allow them in Pocket Core → Links.">
        <dl className="set-dl">
          <div><dt>errors:read</dt><dd className="muted">Your projects, issues and their newest events — Pocket AI answers “what broke today?”, Pocket Day can show it in your journal, Pocket Search finds issues.</dd></div>
          <div><dt>errors:write</dt><dd className="muted">A Pocket app reports its own errors into a project named after it, and may resolve or ignore only that project’s issues.</dd></div>
          <div><dt>events:read</dt><dd className="muted"><code>errors.issue_new</code>, <code>errors.issue_regressed</code>, <code>errors.issue_spike</code>, <code>errors.issue_resolved</code> — for Pocket Flows.</dd></div>
        </dl>
      </Section>
      <div className="row"><button type="button" className="btn primary" onClick={() => save()} disabled={busy}>Save changes</button></div>
    </div>
  )
}

function AITab() {
  const { f, setF, save, busy } = useSettingsForm()
  const { run, status } = useTester()
  const { data: ai } = useAsync(() => api.get<{ ready: boolean; provider: string; detail: string }>(`${V1}/ai/status`), [])
  if (!f) return <Loading />
  const coreOn = f.core_configured && f.core_registered !== false
  const shown = !coreOn && f.ai_provider === 'pocket' ? 'openai' : (f.ai_provider || 'auto')
  return (
    <div className="stack loose">
      <Section title="What the AI does" icon="sparkles" sub="Only when you press its button: Explain on an issue (what happened, the likely cause, where to look and how to fix it — from the newest event's stack, request, steps before it and log lines; kept on the issue) and Sum up on the issues page (a few sentences on the last 24 hours). Passwords, tokens and cookies were taken out on the way in. Questions about everything go to Pocket AI through the Ask button at the top.">
        <SettingRow title="Pocket AI buttons" help="Off: the buttons are gone and nothing is sent to an AI."><Switch checked={f.ai_help} onChange={v => save({ ai_help: v })} label="AI buttons" /></SettingRow>
        {ai && (ai.ready ? <p className="muted small row" style={{ gap: 6 }}><Icon name="sparkles" size={13} />{ai.provider === 'pocket' ? 'Pocket AI' : 'Your OpenAI key'} is ready.</p>
          : <div className="callout info"><Icon name="info" size={15} /><div>No AI connected{ai.detail ? ` (${ai.detail})` : ''}.{!coreOn && <CoreHint />}</div></div>)}
      </Section>
      <Section title="Provider" icon="sparkles">
        <div className="grid" style={{ gap: 12 }}>
          <Field group label="Who writes" help="Automatic prefers your enabled Pocket AI connection; without one it uses your OpenAI settings.">
            <Select ariaLabel="AI provider" value={shown} onChange={v => setF({ ...f, ai_provider: v as SettingsT['ai_provider'] })} options={[{ value: 'auto', label: coreOn ? 'Automatic — prefer Pocket AI' : 'Automatic' }, { value: 'pocket', label: 'Pocket AI', disabled: !coreOn, hint: coreOn ? undefined : 'needs Pocket Core' }, { value: 'openai', label: 'OpenAI / compatible API' }]} />
          </Field>
          {!coreOn && <CoreHint />}
          {coreOn && shown !== 'openai' && <PocketModelSelect value={f.ai_pocket_model || ''} onChange={v => setF({ ...f, ai_pocket_model: v })} />}
          {(shown === 'openai' || (!coreOn && shown === 'auto')) && <>
            <Field label="OpenAI API key" help="platform.openai.com → API keys. Stored on the server; shown masked."><input value={f.openai_api_key} onChange={e => setF({ ...f, openai_api_key: e.target.value })} autoComplete="off" placeholder="sk-…" /></Field>
            <div className="two-col tight">
              <Field label="Model" help="Default gpt-4o-mini."><input value={f.openai_model} onChange={e => setF({ ...f, openai_model: e.target.value })} placeholder="gpt-4o-mini" /></Field>
              <Field label="Base URL (optional)" help="Ollama, LM Studio, OpenRouter…"><input value={f.openai_base_url} onChange={e => setF({ ...f, openai_base_url: e.target.value })} placeholder="https://api.openai.com/v1" /></Field>
            </div>
          </>}
        </div>
      </Section>
      <div className="row wrap">
        <button type="button" className="btn primary" onClick={() => save()} disabled={busy}>Save changes</button>
        {(shown === 'openai' || (!coreOn && shown === 'auto')) && <><button type="button" className="btn" onClick={() => run('openai', f.openai_base_url, f.openai_api_key)} disabled={!f.openai_api_key}>Test the OpenAI key</button>{status('openai')}</>}
      </div>
    </div>
  )
}

function Connection({ app, mode: saved, via, detail, core, onMode, onDisconnect, hasToken, test, direct }: {
  app: string; mode: ConnMode; via: string; detail: string; core: boolean; onMode: (m: ConnMode) => void; onDisconnect: () => void
  hasToken: boolean; test: React.ReactNode; direct: React.ReactNode
}) {
  const [advanced, setAdvanced] = useState(saved === 'direct')
  const mode: ConnMode = !core && saved === 'core' ? 'off' : saved
  const ok = via !== '' && mode !== 'off'
  const showDirect = !core || mode === 'direct' || advanced
  const line = mode === 'off' ? `Off: Errors does not reach ${app}.`
    : mode === 'core' ? (ok ? `Connected through Pocket Core, as you${detail ? ` (${detail})` : ''}. Nothing to fill in; switch it off for yourself in Pocket Core → Settings → Apps acting for me.` : `Not connected: ${detail}.`)
    : (ok ? `Direct connection with your own ${app} URL and token; use Test connection to check it.` : `Not connected: ${detail || 'add the URL and a token'}.`)
  return (
    <div className="conn-block">
      <div className="conn-mode">
        <Segmented size="sm" value={mode} onChange={m => onMode(m as ConnMode)} options={[
          { value: 'core', label: 'Pocket Core', title: core ? 'Through Pocket Core: per person, nothing to paste' : 'Needs Pocket Core: there is none on this box', disabled: !core },
          ...(showDirect ? [{ value: 'direct', label: 'Direct', title: 'Your own URL and API token' }] : []),
          { value: 'off', label: 'Off' },
        ]} />
        {!showDirect && <button type="button" className="conn-advanced" onClick={() => setAdvanced(true)}>Advanced: connect directly with a URL and key</button>}
      </div>
      <div className={`conn-status ${mode === 'off' ? '' : ok ? 'ok' : 'bad'}`}><span className="dot" />{line}</div>
      {mode === 'direct' && <>
        {direct}
        <div className="row wrap">{test}{hasToken && <button type="button" className="btn sm ghost danger-text" onClick={onDisconnect} title="Remove the saved URL and token">Disconnect</button>}</div>
      </>}
      {mode === 'core' && <div className="row wrap">{test}</div>}
    </div>
  )
}

const connBadge = (mode: ConnMode, via: string) => mode === 'off' ? <span className="badge">off</span>
  : via ? <span className="badge ok">{via === 'core' ? 'Pocket Core' : 'direct'}</span>
  : <span className="badge warn">not connected</span>

function Users() {
  const { user, users, reloadMeta, status } = useApp()
  const { toast } = useToast()
  const [edit, setEdit] = useState<Partial<User> | null>(null)
  const [del, setDel] = useState<User | null>(null)
  const isAdmin = user?.role === 'admin'
  const run = async (fn: () => Promise<unknown>, msg: string) => { try { await fn(); await reloadMeta(); toast(msg) } catch (e) { toast((e as Error).message, 'err') } }
  return (
    <Section title="People on this box" actions={isAdmin && <button type="button" className="btn primary sm" onClick={() => setEdit({ role: 'member' })}><Icon name="plus" size={14} />Add person</button>}
      sub="Everyone signs in with their own account and has their own projects, issues, settings and connections. Admins manage accounts; they do not see anyone else’s errors.">
      <div className="set-people">
        {users.map(u => (
          <div key={u.id} className="set-person">
            <Avatar name={u.name} email={u.email} size={34} />
            <div className="grow">
              <div className="set-person-name">{u.name || <span className="muted">—</span>}{u.id === user?.id && <span className="badge">you</span>}{u.role === 'admin' && <span className="badge accent">admin</span>}</div>
              <div className="muted small ellipsis">{u.email}</div>
            </div>
            {isAdmin && <span className="row" style={{ justifyContent: 'flex-end' }}><button type="button" className="btn ghost sm" onClick={() => setEdit(u)}>Edit</button>{u.id !== user?.id && <button type="button" className="btn ghost sm danger-text" onClick={() => setDel(u)}>Remove</button>}</span>}
          </div>
        ))}
      </div>
      {status?.auth_mode !== 'local' && <p className="muted small mt">Auth mode is <code>{status?.auth_mode}</code>: users are created automatically on first visit; passwords are not used here.</p>}
      {edit && <UserModal user={edit} local={status?.auth_mode === 'local'} onClose={() => setEdit(null)} onSaved={() => { setEdit(null); reloadMeta() }} />}
      {del && <Confirm confirmLabel="Remove" title={`Remove ${del.name || del.email}?`} message="Their account is deleted together with their projects and issues." onCancel={() => setDel(null)} onConfirm={() => { const d = del; setDel(null); run(() => api.del(`${V1}/users/${d.id}`), 'User removed') }} />}
    </Section>
  )
}

function UserModal({ user, local, onClose, onSaved }: { user: Partial<User>; local: boolean; onClose: () => void; onSaved: () => void }) {
  const { toast } = useToast()
  const [f, setF] = useState({ name: user.name || '', email: user.email || '', password: '', role: user.role || 'member' })
  const [busy, setBusy] = useState(false)
  const save = async (e: React.FormEvent) => {
    e.preventDefault(); setBusy(true)
    try { if (user.id) await api.put(`${V1}/users/${user.id}`, f); else await api.post(`${V1}/users`, f); toast(user.id ? 'User saved' : 'User added'); onSaved() } catch (e) { toast((e as Error).message, 'err') } finally { setBusy(false) }
  }
  return (
    <Modal title={user.id ? 'Edit person' : 'Add a person'} onClose={onClose} size="sm">
      <form onSubmit={save} className="grid" style={{ gap: 14 }}>
        <Field label="Name"><input value={f.name} onChange={e => setF({ ...f, name: e.target.value })} autoFocus required maxLength={60} /></Field>
        <Field label="Email" help="Used to sign in."><input type="email" value={f.email} onChange={e => setF({ ...f, email: e.target.value })} required /></Field>
        {local && <Field label={user.id ? 'New password (leave empty to keep)' : 'Password'} help="At least 8 characters."><input type="password" value={f.password} onChange={e => setF({ ...f, password: e.target.value })} minLength={user.id ? 0 : 8} required={!user.id} autoComplete="new-password" /></Field>}
        <Field group label="Role"><Segmented value={f.role} onChange={v => setF({ ...f, role: v })} options={[{ value: 'member', label: 'Member' }, { value: 'admin', label: 'Admin' }]} /></Field>
        <div className="form-actions"><button type="button" className="btn" onClick={onClose}>Cancel</button><button className="btn primary" disabled={busy}>{user.id ? 'Save' : 'Add person'}</button></div>
      </form>
    </Modal>
  )
}

function Profile() {
  const { user, setUser, status } = useApp()
  const { toast } = useToast()
  const [f, setF] = useState({ name: user?.name || '', email: user?.email || '', current_password: '', new_password: '' })
  const [busy, setBusy] = useState(false)
  const save = async (e: React.FormEvent) => {
    e.preventDefault(); setBusy(true)
    try { setUser(await api.put(`${V1}/auth/me`, f)); setF(x => ({ ...x, current_password: '', new_password: '' })); toast('Profile saved') } catch (e) { toast((e as Error).message, 'err') } finally { setBusy(false) }
  }
  return (
    <form onSubmit={save} className="stack loose">
      <div className="set-profile">
        <Avatar name={user?.name} email={user?.email} size={56} />
        <div className="grow"><h3>{user?.name || user?.email}</h3><p className="muted small">{user?.email} · {user?.role}</p></div>
      </div>
      <Section title="You">
        <div className="stack">
          <Field label="Name"><input value={f.name} onChange={e => setF({ ...f, name: e.target.value })} /></Field>
          <Field label="Email"><input type="email" value={f.email} onChange={e => setF({ ...f, email: e.target.value })} /></Field>
        </div>
      </Section>
      {status?.auth_mode === 'local' && <Section title="Password" sub="Leave empty to keep the current one."><div className="stack">
        <Field label="Current password"><input type="password" value={f.current_password} onChange={e => setF({ ...f, current_password: e.target.value })} autoComplete="current-password" /></Field>
        <Field label="New password" help="At least 8 characters."><input type="password" value={f.new_password} onChange={e => setF({ ...f, new_password: e.target.value })} autoComplete="new-password" /></Field>
      </div></Section>}
      <div className="row"><button className="btn primary" disabled={busy}>Save changes</button></div>
    </form>
  )
}

function Tokens() {
  const { toast } = useToast()
  const { settings } = useApp()
  const { data, reload } = useAsync(() => api.get<APIToken[]>(`${V1}/tokens`), [])
  const [name, setName] = useState('')
  const [fresh, setFresh] = useState('')
  const [del, setDel] = useState<APIToken | null>(null)
  const create = async (e: React.FormEvent) => {
    e.preventDefault()
    try { const r = await api.post<{ token: string }>(`${V1}/tokens`, { name: name || 'token' }); setFresh(r.token); setName(''); reload() } catch (e) { toast((e as Error).message, 'err') }
  }
  const base = settings?.base_url || window.location.origin
  return (
    <div className="stack loose">
      <Section title="Personal tokens" icon="key" sub={<>Use as <code>Authorization: Bearer &lt;token&gt;</code>. A token acts as you: it can read and change everything, feed a Homepage widget (<code>GET /api/v1/summary</code>) or send an error from a script. See <code>docs/api.md</code>.</>}>
        <form onSubmit={create} className="row mb"><input value={name} onChange={e => setName(e.target.value)} placeholder="Token name (e.g. iPhone Shortcut)" /><button className="btn primary"><Icon name="plus" size={15} />Create</button></form>
        {fresh && <div className="set-fresh mb"><div className="row"><Icon name="check-circle" size={16} /><b>Copy it now — it is shown once.</b></div><pre className="mono">{fresh}</pre></div>}
        {data && data.length > 0 ? (
          <table className="table compact">
            <thead><tr><th>Name</th><th>Prefix</th><th>Last used</th><th>Created</th><th /></tr></thead>
            <tbody>{data.map(t => <tr key={t.id}><td>{t.name}</td><td className="mono">{t.prefix}…</td><td className="muted">{t.last_used_at ? fmtDateTime(t.last_used_at) : 'never'}</td><td className="muted">{fmtDateTime(t.created_at)}</td><td className="right"><button type="button" className="btn ghost sm danger-text" onClick={() => setDel(t)}>Revoke</button></td></tr>)}</tbody>
          </table>
        ) : <p className="muted">No tokens yet.</p>}
        {del && <Confirm confirmLabel="Revoke" title={`Revoke “${del.name}”?`} message="Anything using this token stops working immediately." onCancel={() => setDel(null)} onConfirm={async () => { await api.del(`${V1}/tokens/${del.id}`); setDel(null); reload() }} />}
      </Section>
      <Section title="Endpoints" icon="code" sub={<>At <code>{base}/api/v1</code>. Times are UTC.</>}>
        <dl className="set-dl">
          <div><dt><code>POST /projects/ID/events</code></dt><dd className="muted"><code>{'{"message": "Backup failed", "level": "error"}'}</code> or <code>{'{"type": "ValueError", "value": "…", "stack": "…"}'}</code> — an error from a script or a cron job. Sentry SDKs use the project's DSN instead.</dd></div>
          <div><dt><code>GET /issues?status=open</code></dt><dd className="muted">The issues (<code>open, resolved, ignored, all</code>; <code>project</code>, <code>q</code>, <code>since=24h</code>).</dd></div>
          <div><dt><code>POST /issues/ID/status</code></dt><dd className="muted"><code>{'{"status": "resolved"}'}</code> — say a fix went out, from a deploy script.</dd></div>
          <div><dt><code>GET /issues/ID/markdown</code></dt><dd className="muted">An issue and its newest event as Markdown.</dd></div>
          <div><dt><code>GET /summary</code></dt><dd className="muted">Open, new today, came back and errors in 24 hours — for Homepage.</dd></div>

        </dl>
      </Section>
    </div>
  )
}

function Data() {
  const { user } = useApp()
  const { toast } = useToast()
  const file = React.useRef<HTMLInputElement>(null)
  const bring = async (fl: File) => {
    try { const r = await upload<{ added: Record<string, number> }>(`${V1}/import`, 'file', fl); const a = r.added || {}; toast(`Added ${a.projects || 0} projects, ${a.issues || 0} issues and ${a.events || 0} events`) } catch (e) { toast((e as Error).message, 'err') }
  }
  return (
    <div className="stack loose">
      <Section title="Export" icon="archive" sub="Your projects (with their keys, so apps keep sending after a move), issues with their counts, kept events and links as one JSON file, or the issues as a table for a spreadsheet.">
        <div className="row wrap"><a className="btn" href={`${V1}/export.json`}><Icon name="download" size={15} />Export JSON</a><a className="btn" href={`${V1}/export.csv`}><Icon name="download" size={15} />Issues as CSV</a></div>
      </Section>
      <Section title="Bring an export back" icon="upload" sub="A Pocket Errors export, also from another box. Bringing the same file twice adds nothing twice.">
        <button type="button" className="btn" onClick={() => file.current?.click()}><Icon name="upload" size={15} />Bring a file…</button>
        <input ref={file} type="file" hidden accept=".json,application/json" onChange={e => { const fl = e.target.files?.[0]; e.target.value = ''; if (fl) bring(fl) }} />
      </Section>
      {user?.role === 'admin' && <Section title="Database backup" icon="database" sub="The whole box, every account — admins only.">
        <p className="muted">A consistent copy of the SQLite database (<code>VACUUM INTO</code>). Restore by stopping the app and replacing <code>pocket-errors.db</code> in the data folder.</p>
        <a className="btn" href={`${V1}/backup`}><Icon name="database" size={15} />Download database</a>
      </Section>}
    </div>
  )
}

function About() {
  const { data, error } = useAsync(() => api.get<Record<string, unknown>>(`${V1}/settings/system`), [])
  const [keys, setKeys] = useState(false)
  if (error) return <div className="callout danger"><Icon name="alert" size={15} /><span>{error}</span></div>
  if (!data) return <Loading />
  const counts = (data.counts || {}) as Record<string, number>
  const core = (data.core || {}) as { configured?: boolean; registered?: boolean; url?: string; error?: string }
  const tiles: [string, number | undefined, string][] = [['Projects', counts.projects, 'layers'], ['Issues', counts.issues, 'bug'], ['Events kept', counts.events, 'activity'], ['Tokens', counts.api_tokens, 'key']]
  const rows: [string, React.ReactNode][] = [
    ['Version', <code key="v">{String(data.version)}</code>],
    ['Uptime', String(data.uptime)],
    ['Runtime', `${String(data.go)} · ${String(data.os)}/${String(data.arch)}`],
    ['Time zone', String(data.timezone)],
    ['Sign-in', String(data.auth_mode)],
    ['Pocket Core', core.configured ? (core.registered ? <span key="c" className="ok">connected · {core.url}</span> : <span key="c" className="bad">{core.error || 'not registered yet'}</span>) : <span key="c" className="muted">not configured</span>],
    ['Database', <span key="db"><code>{String(data.db_path)}</code> · {fmtBytes(Number(data.db_bytes))}</span>],
    ['People', `${counts.users ?? 0} accounts on this box`],
  ]
  return (
    <div className="stack loose">
      <div className="set-about-hero">
        <img src="/favicon.svg" alt="" width={48} height={48} />
        <div>
          <h3>Pocket Errors</h3>
          <p className="muted small">Catches the errors of your apps and websites — from any Sentry SDK, from Pocket apps through Pocket Core, or straight from this box’s container logs — and groups them into issues you resolve, ignore or send on. One binary, one SQLite file, one volume.</p>
        </div>
      </div>
      <div className="set-stats">
        {tiles.map(([label, n, icon]) => (
          <div key={label} className="set-stat"><Icon name={icon} size={16} /><span className="set-stat-n tnum">{(n ?? 0).toLocaleString()}</span><span className="set-stat-l">{label}</span></div>
        ))}
      </div>
      <Section title="This server">
        <dl className="set-dl">
          {rows.map(([k, v]) => <div key={k}><dt>{k}</dt><dd>{v}</dd></div>)}
        </dl>
      </Section>
      <div className="row wrap">
        <button type="button" className="btn" onClick={() => setKeys(true)}><Icon name="keyboard" size={15} />Keyboard shortcuts</button>
        <a className="btn" href="https://github.com/abjelosevic88/pocket-homelab-errors" target="_blank" rel="noreferrer"><Icon name="external" size={15} />Project on GitHub</a>
      </div>
      <p className="muted small">Open source (MIT). Part of the Pocket apps: Pocket Core connects them — Alerts, Projects, Tasks, Monitoring, Day, Flows, Pocket AI and more.</p>
      {keys && <ShortcutsSheet onClose={() => setKeys(false)} />}
    </div>
  )
}

