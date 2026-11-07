// Projects: one per app, site or service that sends errors. Each has a key
// (the DSN any Sentry SDK takes), a line to paste, and what to be told about.
import React, { useEffect, useState } from 'react'
import { Link, useNavigate, useParams, useSearchParams } from 'react-router-dom'
import { api, V1 } from '../lib/api'
import { useApp } from '../lib/app-context'
import { announceChange, useData } from '../lib/data'
import { ago, plural } from '../lib/format'
import { PLATFORMS, platformOf, projectIcon, short } from '../lib/errors'
import type { Platform, Project } from '../lib/types'
import { Spark } from '../components/Bars'
import { Icon } from '../components/icons'
import { Confirm, CopyField, copyText, Empty, errMsg, Field, Loading, Modal, PageHeader, SettingRow, Spinner, Switch, useToast } from '../components/ui'
import { openNewProject, useCrumb } from '../components/Layout'

export default function Projects() {
  const { projects } = useData()
  const [params, setParams] = useSearchParams()
  const navigate = useNavigate()
  const [archived, setArchived] = useState(false)
  const neu = params.get('new') === '1'
  const list = (projects || []).filter(p => archived || !p.archived)
  const hasArchived = (projects || []).some(p => p.archived)
  return (
    <div className="page">
      <PageHeader title="Projects" sub={projects ? plural(list.length, 'project') : ' '}
        actions={<button type="button" className="btn primary sm" onClick={openNewProject}><Icon name="plus" size={14} />New project</button>} />
      {!projects ? <Loading /> : !list.length ? (
        <Empty icon="layers" title="No projects yet">A project is one website, app or service. Make one and paste a line into it — or let Errors read this box’s containers (Settings → This box).</Empty>
      ) : (
        <div className="project-grid">
          {list.map(p => <ProjectCard key={p.id} p={p} />)}
        </div>
      )}
      {hasArchived && <div className="row center"><button type="button" className="btn ghost sm" onClick={() => setArchived(a => !a)}>{archived ? 'Hide archived projects' : 'Show archived projects'}</button></div>}
      {neu && <NewProject onClose={() => { params.delete('new'); setParams(params, { replace: true }) }} onMade={p => navigate(`/projects/${p.id}?made=1`)} />}
    </div>
  )
}

function ProjectCard({ p }: { p: Project }) {
  return (
    <div className={`project-card ${p.archived ? 'archived' : ''}`}>
      <Link to={`/?project=${p.id}`} className="pc-main">
      <div className="pc-head">
        <span className="pc-ico"><Icon name={projectIcon(p)} size={17} /></span>
        <span className="grow ellipsis pc-name">{p.name}</span>
        {p.archived ? <span className="badge">archived</span> : p.open > 0 ? <span className="badge warn">{p.open} open</span> : <span className="badge ok"><Icon name="check" size={11} />quiet</span>}
      </div>
      <div className="pc-sub muted small ellipsis">{p.container ? `Container ${p.container}` : p.source ? 'Reports through Pocket Core' : platformOf(p.platform).label}</div>
      <div className="pc-foot">
        <Spark values={p.hours} width={120} height={26} />
        <span className="muted small"><b className="tnum">{short(p.events_24h)}</b> in 24 h · {p.last_event_at ? `last ${ago(p.last_event_at)}` : 'nothing yet'}</span>
      </div>
      </Link>
      <Link to={`/projects/${p.id}`} className="pc-settings btn ghost icon-only sm" aria-label={`${p.name}: settings and setup`} title="Settings and setup"><Icon name="settings" size={15} /></Link>
    </div>
  )
}

function NewProject({ onClose, onMade }: { onClose: () => void; onMade: (p: Project) => void }) {
  const { toast } = useToast()
  const { settings } = useApp()
  const [name, setName] = useState('')
  const [platform, setPlatform] = useState<Platform>('javascript')
  const [busy, setBusy] = useState(false)
  const make = async (e: React.FormEvent) => {
    e.preventDefault(); setBusy(true)
    try { const p = await api.post<Project>(`${V1}/projects`, { name, platform }); announceChange(); onMade(p) } catch (err) { toast(errMsg(err), 'err') } finally { setBusy(false) }
  }
  return (
    <Modal title="New project" onClose={onClose}>
      <form onSubmit={make} className="grid" style={{ gap: 14 }}>
        <Field label="Name" help="The site, app or service: “Shop website”, “Booking API”."><input value={name} onChange={e => setName(e.target.value)} required maxLength={120} autoFocus /></Field>
        <Field group label="What is it">
          <div className="platform-pick">
            {PLATFORMS.filter(x => x.value !== 'docker').map(x => (
              <button key={x.value} type="button" className={`platform-opt ${platform === x.value ? 'active' : ''}`} aria-pressed={platform === x.value} onClick={() => setPlatform(x.value)}>
                <Icon name={x.icon} size={18} /><span className="grow"><b>{x.label}</b><span className="muted tiny">{x.sub}</span></span>
              </button>
            ))}
          </div>
        </Field>
        {settings?.is_admin && <p className="muted small"><Icon name="package" size={13} className="inline-ico" /> A container on this box needs no project: switch on <Link to="/settings/box" onClick={onClose}>Settings → This box</Link> and each one gets its project with its first error.</p>}
        <div className="form-actions"><button type="button" className="btn" onClick={onClose}>Cancel</button><button className="btn primary" disabled={busy || !name.trim()}>{busy && <Spinner />}Make it</button></div>
      </form>
    </Modal>
  )
}

// ---- one project ----

export function ProjectPage() {
  const { id } = useParams()
  const [params] = useSearchParams()
  const navigate = useNavigate()
  const { toast } = useToast()
  const [p, setP] = useState<Project | null>(null)
  const [f, setF] = useState<Project | null>(null)
  const [err, setErr] = useState('')
  const [busy, setBusy] = useState(false)
  const [del, setDel] = useState(false)
  const [rekey, setRekey] = useState(false)
  useCrumb(p?.name)
  useEffect(() => { api.get<Project>(`${V1}/projects/${id}`).then(x => { setP(x); setF(x) }).catch(e => setErr(errMsg(e))) }, [id])
  if (err) return <Empty icon="alert" title="This project is not here">{err === 'not found' ? 'It may have been deleted, or it belongs to someone else.' : err}</Empty>
  if (!p || !f) return <Loading />
  const save = async (patch: Partial<Project> & { new_key?: boolean }) => {
    setBusy(true)
    try { const x = await api.put<Project>(`${V1}/projects/${p.id}`, { ...patch }); setP(x); setF(x); announceChange(); toast('Saved') } catch (e) { toast(errMsg(e), 'err') } finally { setBusy(false) }
  }
  const test = async () => {
    setBusy(true)
    try { const r = await api.post<{ issue_id: number }>(`${V1}/projects/${p.id}/test`); announceChange(); toast('A test error arrived', undefined); navigate(`/issues/${r.issue_id}`) } catch (e) { toast(errMsg(e), 'err') } finally { setBusy(false) }
  }
  const container = p.source === 'docker'
  const viaLink = !!p.source && !container
  return (
    <div className="page project-page">
      <PageHeader title={p.name} sub={container ? `Container ${p.container} on this box` : viaLink ? 'A Pocket app reporting through Pocket Core' : platformOf(p.platform).label}
        actions={<><Link className="btn sm" to={`/?project=${p.id}`}><Icon name="inbox" size={14} />Issues{p.open ? ` (${p.open})` : ''}</Link><button type="button" className="btn sm" onClick={test} disabled={busy}><Icon name="zap" size={14} />Send a test error</button></>} />
      {params.get('made') === '1' && <div className="callout success"><Icon name="check-circle" size={15} /><div><b>Made.</b> Paste the line below into {p.name}; its errors show up under Issues as they happen. <b>Send a test error</b> to see one arrive.</div></div>}

      {container ? (
        <section className="card"><div className="card-head"><div className="grow"><h2>Read from its logs</h2><p className="muted small">Errors reads what <code>{p.container}</code> writes and keeps its error lines, Go panics and Python tracebacks. Nothing to install in it; archive this project to stop reading it.</p></div></div>
          <div className="card-body">
            <SettingRow title="Count warnings too" help="Off: only error lines, panics and tracebacks."><Switch checked={f.min_level === 'warning'} onChange={v => save({ min_level: v ? 'warning' : 'error' })} label="Warnings too" /></SettingRow>
          </div>
        </section>
      ) : viaLink ? (
        <section className="card"><div className="card-head"><div className="grow"><h2>Through Pocket Core</h2><p className="muted small">This Pocket app reports its own errors through its Pocket Core link (<code>errors:write</code>); there is nothing to paste. Switch the link off in Pocket Core → Links to stop it.</p></div></div></section>
      ) : <Setup p={p} />}

      <section className="card">
        <div className="card-head"><div className="grow"><h2>Tell me</h2><p className="muted small">How you are told is in Settings → Telling you (Pocket Alerts, your own push, or both).</p></div></div>
        <div className="card-body">
          <SettingRow title="About a new error" help="The first time a problem happens."><Switch checked={f.notify_new} onChange={v => save({ notify_new: v })} label="New errors" /></SettingRow>
          <SettingRow title="When a resolved one comes back"><Switch checked={f.notify_again} onChange={v => save({ notify_again: v })} label="Come back" /></SettingRow>
          <SettingRow title="When one happens a lot" help="This many times in an hour, at most every six hours per issue. 0 = never.">
            <input className="num-sm" type="number" min={0} max={100000} value={f.notify_spike} onChange={e => setF({ ...f, notify_spike: Number(e.target.value) })} onBlur={() => f.notify_spike !== p.notify_spike && save({ notify_spike: f.notify_spike })} aria-label="Errors an hour" />
          </SettingRow>
        </div>
      </section>

      <section className="card">
        <div className="card-head"><div className="grow"><h2>Project</h2></div></div>
        <div className="card-body stack">
          <Field label="Name"><input value={f.name} onChange={e => setF({ ...f, name: e.target.value })} onBlur={() => f.name.trim() && f.name !== p.name && save({ name: f.name })} maxLength={120} /></Field>
          <SettingRow title="Errors a minute" help={`More than this in one minute are turned away, so a loop cannot fill the disk. 0 = the usual 600.`}>
            <input className="num-sm" type="number" min={0} max={100000} value={f.rate_limit} onChange={e => setF({ ...f, rate_limit: Number(e.target.value) })} onBlur={() => f.rate_limit !== p.rate_limit && save({ rate_limit: f.rate_limit })} aria-label="Errors a minute" />
          </SettingRow>
          <SettingRow title="Archived" help="An archived project takes nothing in (a watched container is no longer read) and leaves the sidebar; its issues stay."><Switch checked={f.archived} onChange={v => save({ archived: v })} label="Archived" /></SettingRow>
          <div className="row wrap">
            {!container && !viaLink && <button type="button" className="btn" onClick={() => setRekey(true)} disabled={busy}><Icon name="refresh" size={14} />New key</button>}
            <button type="button" className="btn danger-text" onClick={() => setDel(true)}><Icon name="trash" size={14} />Delete the project</button>
          </div>
        </div>
      </section>
      {rekey && <Confirm title="Make a new key?" message="The old DSN stops working at once: every app using it has to get the new one." confirmLabel="New key" onCancel={() => setRekey(false)} onConfirm={() => { setRekey(false); save({ new_key: true }) }} />}
      {del && <Confirm title={`Delete ${p.name}?`} message="Its issues and events go with it. A watched container gets a new project with its next error unless you switch watching off." confirmLabel="Delete" onCancel={() => setDel(false)}
        onConfirm={async () => { setDel(false); try { await api.del(`${V1}/projects/${p.id}`); announceChange(); toast('Project deleted'); navigate('/projects') } catch (e) { toast(errMsg(e), 'err') } }} />}
    </div>
  )
}

function Setup({ p: raw }: { p: Project }) {
  const { toast } = useToast()
  // without BASE_URL the server only knows paths: this page's address fills them in
  const abs = (u: string) => u.startsWith('/') ? location.origin + u : u
  const p = { ...raw, script_url: abs(raw.script_url), store_url: abs(raw.store_url) }
  const dsn = p.dsn_local || p.dsn
  const snippets: { id: string; label: string; lang: string; code: string; note?: string }[] = []
  if (p.platform === 'javascript' || p.platform === 'other') {
    snippets.push({ id: 'script', label: 'One line in the page', lang: 'html', code: `<script src="${p.script_url}" data-dsn="${p.dsn}" data-release="1.0.0"></script>`,
      note: 'Put it in the <head>, before your own scripts. It sends uncaught errors and failed promises with the clicks before them; PocketErrors.capture(err) sends one yourself.' })
    snippets.push({ id: 'sentry-js', label: 'Or with the Sentry SDK', lang: 'js', code: `import * as Sentry from "@sentry/browser";\n\nSentry.init({\n  dsn: "${p.dsn}",\n  release: "my-site@1.0.0",\n});` })
  }
  if (p.platform === 'go') snippets.push({ id: 'go', label: 'Go (sentry-go)', lang: 'go', code: `import "github.com/getsentry/sentry-go"\n\nsentry.Init(sentry.ClientOptions{\n\tDsn:     "${dsn}",\n\tRelease: "my-app@1.0.0",\n})\ndefer sentry.Flush(2 * time.Second)\n\n// with chi or net/http: sentryhttp.New(sentryhttp.Options{Repanic: true}).Handle(router)` })
  if (p.platform === 'python') snippets.push({ id: 'py', label: 'Python (sentry-sdk)', lang: 'python', code: `import sentry_sdk\n\nsentry_sdk.init(\n    dsn="${dsn}",\n    release="my-app@1.0.0",\n)` })
  if (p.platform === 'node') snippets.push({ id: 'node', label: 'Node.js (@sentry/node)', lang: 'js', code: `const Sentry = require("@sentry/node");\n\nSentry.init({\n  dsn: "${dsn}",\n  release: "my-app@1.0.0",\n});` })
  snippets.push({ id: 'curl', label: p.platform === 'other' ? 'Plain JSON (scripts, cron jobs)' : 'Or plain JSON from a script', lang: 'sh',
    code: `curl -X POST ${p.store_url} \\\n  -H "Authorization: Bearer YOUR_TOKEN" \\\n  -H "Content-Type: application/json" \\\n  -d '{"message": "Nightly backup failed", "level": "error", "extra": {"disk": "/data"}}'`,
    note: 'A token is made in Settings → API & shortcuts. Send "type", "value" and "stack" (as text) for an error with its stack.' })
  return (
    <section className="card">
      <div className="card-head"><div className="grow"><h2>Send errors here</h2><p className="muted small">Any Sentry SDK works with this DSN — nothing else to change.</p></div></div>
      <div className="card-body stack">
        <Field group label="DSN" help={p.dsn_local ? <>From another container on this box use <code>{p.dsn_local}</code>.</> : undefined}><CopyField value={p.dsn} /></Field>
        {snippets.map(s => (
          <div key={s.id} className="snippet">
            <div className="snippet-head"><b>{s.label}</b><button type="button" className="btn ghost sm" onClick={async () => toast(await copyText(s.code) ? 'Copied' : 'Could not copy')}><Icon name="copy" size={13} />Copy</button></div>
            <pre className="code">{s.code}</pre>
            {s.note && <p className="muted small">{s.note}</p>}
          </div>
        ))}
      </div>
    </section>
  )
}
