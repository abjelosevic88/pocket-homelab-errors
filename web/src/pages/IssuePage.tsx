// One issue: what broke, where, how often, its events one by one with the
// stack, what happened before, the request — and what to do with it.
import React, { useCallback, useEffect, useMemo, useState } from 'react'
import { Link, useNavigate, useParams } from 'react-router-dom'
import { api, V1 } from '../lib/api'
import { useApp } from '../lib/app-context'
import { announceChange, useAppState } from '../lib/data'
import { ago, fmtDateTime, plural } from '../lib/format'
import { APP_INFO, hourLabels, IGNORE_FOR, LEVELS, splitTitle, STATE_WORDS } from '../lib/errors'
import type { Breadcrumb, ErrorEvent, EventPage, Exception, Frame, IssueLink, IssueView, Status } from '../lib/types'
import { Bars } from '../components/Bars'
import { Icon } from '../components/icons'
import { Select } from '../components/Select'
import { type Anchor, Confirm, copyText, Empty, errMsg, Field, Loading, Menu, type MenuItem, Modal, modalOpen, rectOf, Segmented, Spinner, useIsMobile, useToast } from '../components/ui'
import { useCrumb } from '../components/Layout'

export default function IssuePage() {
  const { id } = useParams()
  const navigate = useNavigate()
  const { toast } = useToast()
  const { settings } = useApp()
  const [is, setIs] = useState<IssueView | null>(null)
  const [error, setError] = useState('')
  const [busy, setBusy] = useState(false)
  const [menu, setMenu] = useState<{ which: 'ignore' | 'send' | 'more'; at: Anchor } | null>(null)
  const [send, setSend] = useState<'projects' | 'tasks' | null>(null)
  const [del, setDel] = useState(false)
  const [ai, setAI] = useState<'idle' | 'busy'>('idle')
  const appState = useAppState()
  useCrumb(is ? is.project_name : undefined)

  const load = useCallback(async () => {
    try { setIs(await api.get<IssueView>(`${V1}/issues/${id}`)); setError('') } catch (e) { setError(errMsg(e)) }
  }, [id])
  useEffect(() => { setIs(null); load() }, [load])

  const setStatus = async (to: Status, forLen = '') => {
    if (!is) return
    setBusy(true)
    try {
      await api.post(`${V1}/issues/${is.id}/status`, { status: to, for: forLen })
      toast(to === 'resolved' ? 'Resolved: if it happens again it comes back' : to === 'ignored' ? 'Ignored' : 'Open again')
      announceChange(); await load()
    } catch (e) { toast(errMsg(e), 'err') } finally { setBusy(false) }
  }
  const explain = async (again = false) => {
    if (!is) return
    setAI('busy')
    try { const r = await api.post<{ text: string; at: string }>(`${V1}/issues/${is.id}/explain`, { again }); setIs({ ...is, ai_text: r.text, ai_at: r.at }) } catch (e) { toast(errMsg(e), 'err') } finally { setAI('idle') }
  }
  const copyMd = async () => {
    if (!is) return
    const md = await api.getText(`${V1}/issues/${is.id}/markdown`).catch(() => '')
    toast(md && await copyText(md) ? 'Copied as Markdown' : 'Could not copy', md ? undefined : 'err')
  }

  useEffect(() => {
    const h = (e: KeyboardEvent) => {
      const t = e.target as HTMLElement
      if (!is || e.metaKey || e.ctrlKey || e.altKey || t.closest('input, textarea, [contenteditable="true"]') || modalOpen() || document.querySelector('.menu, .sel-pop')) return
      if (e.key === 'e' && is.status !== 'resolved') { e.preventDefault(); setStatus('resolved') }
      else if (e.key === 'o' && is.status !== 'open') { e.preventDefault(); setStatus('open') }
    }
    window.addEventListener('keydown', h)
    return () => window.removeEventListener('keydown', h)
  })

  if (error) return <Empty icon="alert" title="This issue is not here">{error === 'not found' ? 'It may have been deleted, or it belongs to someone else.' : error}</Empty>
  if (!is) return <Loading />
  const [type, msg] = splitTitle(is.title, is.type)
  const proj = appState('projects', 'projects:write'), tasks = appState('tasks', 'tasks:write')
  const coreOn = !!settings?.core_configured && settings.core_registered !== false
  const sendItems: MenuItem[] = [
    { label: <span className="col"><span>Pocket Projects issue</span>{!proj.ok && <span className="muted tiny">{coreOn ? (proj.app ? STATE_WORDS[proj.app.state] : 'checking…') : 'Needs Pocket Core'}</span>}</span>, icon: 'check-square', disabled: !proj.ok, onSelect: () => setSend('projects') },
    { label: <span className="col"><span>Pocket Tasks task</span>{!tasks.ok && <span className="muted tiny">{coreOn ? (tasks.app ? STATE_WORDS[tasks.app.state] : 'checking…') : 'Needs Pocket Core'}</span>}</span>, icon: 'check-circle', disabled: !tasks.ok, onSelect: () => setSend('tasks') },
    'sep',
    { label: 'Copy as Markdown', icon: 'copy', onSelect: copyMd },
    { label: 'Copy the link', icon: 'link', onSelect: async () => { toast(await copyText(is.url || location.href) ? 'Link copied' : 'Could not copy') } },
  ]

  return (
    <div className="page issue-page">
      <header className="issue-head">
        <div className="issue-head-title">
          <h1><span className={`lv-dot lv-${is.level}`} />{type && <b>{type}</b>}{type && msg ? ': ' : ''}<span>{msg || (!type ? is.title : '')}</span></h1>
          <div className="issue-head-sub">
            <span className={`pill lv-pill lv-${is.level}`}><Icon name={LEVELS[is.level]?.icon || 'alert'} size={11} />{LEVELS[is.level]?.label}</span>
            {is.status === 'open' && is.regressed && <span className="pill st-back"><Icon name="rotate-ccw" size={11} />Came back</span>}
            {is.status === 'open' && is.new && <span className="pill st-new">New</span>}
            {is.status === 'resolved' && <span className="pill st-done"><Icon name="check" size={11} />Resolved {is.resolved_at ? ago(is.resolved_at) : ''}</span>}
            {is.status === 'ignored' && <span className="pill"><Icon name="eye-off" size={11} />Ignored{is.ignored_until ? ` until ${fmtDateTime(is.ignored_until)}` : ' for good'}</span>}
            <Link to={`/?project=${is.project_id}`} className="issue-proj-link"><Icon name="layers" size={12} />{is.project_name}</Link>
            {is.culprit && <span className="mono muted ellipsis">{is.culprit}</span>}
          </div>
        </div>
        <div className="issue-actions">
          <button type="button" className="btn primary sm" disabled={busy || is.status === 'resolved'} onClick={() => setStatus('resolved')} title="Resolve (E)"><Icon name="check" size={14} />Resolve</button>
          <button type="button" className="btn sm" disabled={busy || is.status === 'ignored'} onClick={e => setMenu({ which: 'ignore', at: rectOf(e.currentTarget) })}><Icon name="eye-off" size={14} />Ignore</button>
          <button type="button" className="btn sm" disabled={busy || is.status === 'open'} onClick={() => setStatus('open')} title="Open again (O)"><Icon name="undo" size={14} />Open again</button>
          <button type="button" className="btn sm" onClick={e => setMenu({ which: 'send', at: rectOf(e.currentTarget) })}><Icon name="send" size={14} />Send to</button>
          {settings?.ai_help && <button type="button" className="btn sm" disabled={ai === 'busy'} onClick={() => explain(false)} title="Pocket AI explains what went wrong and how to fix it">{ai === 'busy' ? <Spinner /> : <Icon name="sparkles" size={14} />}Explain</button>}
          <button type="button" className="btn ghost icon-only sm" aria-label="More" onClick={e => setMenu({ which: 'more', at: rectOf(e.currentTarget) })}><Icon name="more" size={16} /></button>
        </div>
      </header>
      {menu?.which === 'ignore' && <Menu anchor={menu.at} onClose={() => setMenu(null)} items={IGNORE_FOR.map(o => ({ label: o.label, onSelect: () => setStatus('ignored', o.value) }))} />}
      {menu?.which === 'send' && <Menu anchor={menu.at} onClose={() => setMenu(null)} width={260} items={sendItems} />}
      {menu?.which === 'more' && <Menu anchor={menu.at} onClose={() => setMenu(null)} align="right" items={[{ label: 'Copy as Markdown', icon: 'copy', onSelect: copyMd }, 'sep', { label: 'Delete this issue', icon: 'trash', danger: true, onSelect: () => setDel(true) }]} />}

      <div className="issue-grid">
        <div className="issue-col">
          {(is.ai_text || ai === 'busy') && (
            <section className="card ai-card">
              <div className="card-head"><div className="grow"><h2><Icon name="sparkles" size={15} /> What Pocket AI makes of it</h2>{is.ai_at && <p className="muted small">{ago(is.ai_at)} · from the newest event; check before you change code</p>}</div>
                <button type="button" className="btn ghost sm" disabled={ai === 'busy'} onClick={() => explain(true)}><Icon name="refresh" size={13} />Ask again</button></div>
              <div className="card-body">{ai === 'busy' && !is.ai_text ? <Loading label="Reading the stack…" /> : <Markdownish text={is.ai_text} />}</div>
            </section>
          )}
          <EventView issue={is} />
        </div>
        <aside className="issue-side">
          <Stats is={is} />
          <Chart id={is.id} />
          <Links is={is} onChange={load} />
          <Note is={is} onSaved={note => setIs({ ...is, note })} />
          <TagCounts tags={is.tags} />
        </aside>
      </div>

      {send && <SendModal is={is} to={send} onClose={() => setSend(null)} onSent={() => { setSend(null); load() }} />}
      {del && <Confirm title="Delete this issue?" message="Its events go with it. If it happens again it comes back as a new issue." confirmLabel="Delete" onCancel={() => setDel(false)}
        onConfirm={async () => { setDel(false); try { await api.del(`${V1}/issues/${is.id}`); announceChange(); toast('Issue deleted'); navigate('/') } catch (e) { toast(errMsg(e), 'err') } }} />}
    </div>
  )
}

function Stats({ is }: { is: IssueView }) {
  const rows: [string, React.ReactNode][] = [
    ['Times', <span key="t" className="tnum">{is.times.toLocaleString()}</span>],
    ['People', <span key="p" className="tnum">{is.people ? is.people.toLocaleString() : '—'}</span>],
    ['First seen', <span key="f">{ago(is.first_seen)}{is.first_release && <span className="muted"> · {is.first_release}</span>}</span>],
    ['Last seen', <span key="l">{ago(is.last_seen)}{is.last_release && <span className="muted"> · {is.last_release}</span>}</span>],
  ]
  if (is.environment) rows.push(['Environment', is.environment])
  return (
    <section className="card side-card">
      <dl className="kv">{rows.map(([k, v]) => <div key={k}><dt>{k}</dt><dd>{v}</dd></div>)}</dl>
    </section>
  )
}

function Chart({ id }: { id: number }) {
  const [range, setRange] = useState<'24h' | '30d'>('24h')
  const [data, setData] = useState<{ from: string; counts: number[] } | null>(null)
  useEffect(() => { api.get<{ from: string; counts: number[] }>(`${V1}/issues/${id}/hours?range=${range}`).then(setData).catch(() => setData(null)) }, [id, range])
  const labels = useMemo(() => {
    if (range === '24h') return hourLabels(24)
    const out: string[] = []
    const d = new Date(); d.setHours(0, 0, 0, 0)
    for (let i = 29; i >= 0; i--) { const t = new Date(d.getTime() - i * 86400_000); out.push(t.toLocaleDateString(undefined, { day: 'numeric', month: 'short' })) }
    return out
  }, [range])
  return (
    <section className="card side-card">
      <div className="side-head"><h3>How often</h3><Segmented size="sm" value={range} onChange={setRange} options={[{ value: '24h', label: '24 h' }, { value: '30d', label: '30 days' }]} /></div>
      {data ? <Bars labels={labels} series={[{ key: 'n', label: 'Errors', values: data.counts, className: 'b-error' }]} height={70} every={range === '24h' ? 6 : 7} /> : <div className="chart-ph" />}
    </section>
  )
}

function Links({ is, onChange }: { is: IssueView; onChange: () => void }) {
  const { toast } = useToast()
  if (!is.links.length) return null
  return (
    <section className="card side-card">
      <div className="side-head"><h3>In your other apps</h3></div>
      <ul className="link-list">
        {is.links.map((l: IssueLink) => (
          <li key={l.id}>
            <Icon name={APP_INFO[l.app]?.icon || 'link'} size={14} />
            <span className="grow ellipsis">{l.url ? <a href={l.url} target="_blank" rel="noreferrer">{l.ref ? <b>{l.ref}</b> : null} {l.title}</a> : <>{l.ref && <b>{l.ref}</b>} {l.title}</>}<span className="muted tiny block-line">{APP_INFO[l.app]?.name || l.app} · {ago(l.created_at)}</span></span>
            <button type="button" className="btn ghost icon-only sm" aria-label="Forget this link" title="Forget this link (it stays in the other app)" onClick={async () => { try { await api.del(`${V1}/issues/${is.id}/links/${l.id}`); onChange() } catch (e) { toast(errMsg(e), 'err') } }}><Icon name="x" size={13} /></button>
          </li>
        ))}
      </ul>
    </section>
  )
}

function Note({ is, onSaved }: { is: IssueView; onSaved: (n: string) => void }) {
  const { toast } = useToast()
  const [v, setV] = useState(is.note)
  useEffect(() => setV(is.note), [is.id, is.note])
  const save = async () => {
    if (v === is.note) return
    try { await api.put(`${V1}/issues/${is.id}`, { note: v }); onSaved(v); toast('Note saved') } catch (e) { toast(errMsg(e), 'err') }
  }
  return (
    <section className="card side-card">
      <div className="side-head"><h3>Note</h3></div>
      <textarea value={v} onChange={e => setV(e.target.value)} onBlur={save} rows={3} placeholder="Who looks at it, what you found…" aria-label="Note" />
    </section>
  )
}

function TagCounts({ tags }: { tags: Record<string, Record<string, number>> }) {
  const keys = Object.keys(tags).filter(k => !['container', 'image'].includes(k)).sort((a, b) => (a === 'release' ? -1 : b === 'release' ? 1 : a.localeCompare(b)))
  if (!keys.length) return null
  return (
    <section className="card side-card">
      <div className="side-head"><h3>Tags</h3><span className="muted tiny">in the kept events</span></div>
      <div className="tag-counts">
        {keys.slice(0, 10).map(k => {
          const vals = Object.entries(tags[k]).sort((a, b) => b[1] - a[1])
          const total = vals.reduce((s, [, n]) => s + n, 0)
          return (
            <div key={k} className="tag-count">
              <div className="tag-k">{k}</div>
              {vals.slice(0, 3).map(([val, n]) => (
                <div key={val} className="tag-v" title={`${val}: ${n}`}>
                  <span className="tag-bar" style={{ width: `${Math.round((n / total) * 100)}%` }} />
                  <span className="ellipsis">{val}</span><span className="muted tnum">{Math.round((n / total) * 100)}%</span>
                </div>
              ))}
            </div>
          )
        })}
      </div>
    </section>
  )
}

// ---- the events ----

function EventView({ issue }: { issue: IssueView }) {
  const [page, setPage] = useState<EventPage | null>(null)
  const [eid, setEid] = useState(0)
  useEffect(() => {
    api.get<EventPage>(eid ? `${V1}/issues/${issue.id}/events/${eid}` : `${V1}/issues/${issue.id}/latest`).then(setPage).catch(() => setPage({ event: null, pos: 0, total: 0, newer: 0, older: 0 }))
  }, [issue.id, issue.times, eid])
  useEffect(() => {
    const h = (e: KeyboardEvent) => {
      const t = e.target as HTMLElement
      if (!page || e.metaKey || e.ctrlKey || e.altKey || t.closest('input, textarea') || modalOpen() || document.querySelector('.menu, .sel-pop')) return
      if (e.key === 'j' && page.older) { e.preventDefault(); setEid(page.older) }
      else if (e.key === 'k' && page.newer) { e.preventDefault(); setEid(page.newer) }
    }
    window.addEventListener('keydown', h)
    return () => window.removeEventListener('keydown', h)
  })
  if (!page) return <Loading />
  const ev = page.event
  return (
    <section className="card event-card">
      <div className="event-nav">
        <div className="grow">
          <h2>{ev ? (page.pos === 1 ? 'The newest time' : `Event ${page.pos} of ${page.total}`) : 'Events'}</h2>
          {ev && <p className="muted small">{fmtDateTime(ev.at)} · {ago(ev.at)}{ev.release && ` · ${ev.release}`}{ev.environment && ` · ${ev.environment}`}{ev.who && ` · ${ev.who}`}</p>}
        </div>
        <button type="button" className="btn sm" disabled={!page.newer} onClick={() => setEid(page.newer)} title="Newer (K)"><Icon name="chevron-left" size={14} />Newer</button>
        <button type="button" className="btn sm" disabled={!page.older} onClick={() => setEid(page.older)} title="Older (J)">Older<Icon name="chevron-right" size={14} /></button>
      </div>
      {!ev ? <p className="muted pad">The events of this issue were tidied away (Settings → General → Keep events); the counts stay.</p> : <EventBody ev={ev} />}
    </section>
  )
}

function EventBody({ ev }: { ev: ErrorEvent }) {
  const d = ev.data
  const exs = [...(d.exceptions || [])].reverse()
  const msgShown = d.message && !exs.some(e => (e.value || '').trim() === d.message!.trim())
  return (
    <div className="event-body">
      {exs.map((e, i) => <ExceptionView key={i} e={e} cause={i > 0} />)}
      {msgShown && <Sect title="Message"><pre className="pre-wrap">{d.message}</pre></Sect>}
      {!!d.log_lines?.length && <Sect title="Log" sub={d.server_name ? `container ${d.server_name}` : undefined}><pre className="log-lines">{d.log_lines.join('\n')}</pre></Sect>}
      {!!d.breadcrumbs?.length && <Crumbs list={d.breadcrumbs} />}
      {d.request && (d.request.url || d.request.method) && <Sect title="Request">
        <p className="mono req-line"><b>{d.request.method}</b> {d.request.url}</p>
        {d.request.query && <KV rows={[['Query', d.request.query]]} />}
        {!!d.request.headers?.length && <KV rows={d.request.headers} />}
        {d.request.data && <pre className="pre-wrap small">{d.request.data}</pre>}
      </Sect>}
      {d.user && <Sect title="The app’s user"><KV rows={Object.entries(d.user)} /></Sect>}
      {!!d.tags?.length && <Sect title="Tags"><div className="chips">{d.tags.map(([k, v]) => <span key={k + v} className="chip"><span className="muted">{k}</span> {v}</span>)}</div></Sect>}
      {d.contexts && Object.keys(d.contexts).length > 0 && <Sect title="Where it ran"><div className="ctx-grid">{Object.entries(d.contexts).map(([name, kv]) => <div key={name}><h4>{name}</h4><KV rows={kv} /></div>)}</div></Sect>}
      {d.extra && <Sect title="Extra"><pre className="pre-wrap small">{JSON.stringify(d.extra, null, 2)}</pre></Sect>}
      {(d.sdk || d.logger || d.transaction) && <p className="muted tiny event-meta">{[d.transaction && `in ${d.transaction}`, d.logger && `logger ${d.logger}`, d.sdk && `sent by ${d.sdk}`, `event ${ev.event_id}`].filter(Boolean).join(' · ')}</p>}
    </div>
  )
}

function Sect({ title, sub, children }: { title: string; sub?: string; children: React.ReactNode }) {
  return <div className="ev-sect"><h3>{title}{sub && <span className="muted tiny"> {sub}</span>}</h3>{children}</div>
}

function KV({ rows }: { rows: [string, string][] }) {
  return <dl className="kv small">{rows.map(([k, v], i) => <div key={k + i}><dt>{k}</dt><dd className="mono">{v}</dd></div>)}</dl>
}

function ExceptionView({ e, cause }: { e: Exception; cause: boolean }) {
  const frames = [...(e.frames || [])].reverse() // the failing frame first
  const [all, setAll] = useState(false)
  const anyApp = frames.some(f => f.in_app)
  // library frames between the app's own fold into one line, unless "all"
  const groups: (Frame | { hidden: Frame[] })[] = []
  frames.forEach((f, i) => {
    if (all || !anyApp || f.in_app || i === 0) { groups.push(f); return }
    const last = groups[groups.length - 1]
    if (last && 'hidden' in last) last.hidden.push(f)
    else groups.push({ hidden: [f] })
  })
  return (
    <div className="ev-sect exception">
      <h3>{cause && <span className="muted">Caused by </span>}<b>{e.type || 'Error'}</b>{e.mechanism && <span className="muted tiny"> · {e.mechanism}{e.handled === false ? ', not handled' : ''}</span>}</h3>
      {e.value && <pre className="ex-value pre-wrap">{e.value}</pre>}
      {frames.length > 0 && <>
        <div className="frames">
          {groups.map((g, i) => 'hidden' in g
            ? <button key={i} type="button" className="frames-fold" onClick={() => setAll(true)}><Icon name="more" size={13} />{plural(g.hidden.length, 'library frame')}</button>
            : <FrameRow key={i} f={g} open={i === 0 && g.in_app} />)}
        </div>
        {anyApp && frames.some(f => !f.in_app) && <button type="button" className="link-btn small" onClick={() => setAll(a => !a)}>{all ? 'Only the app’s own frames' : 'Show every frame'}</button>}
      </>}
    </div>
  )
}

function FrameRow({ f, open: start }: { f: Frame; open: boolean }) {
  const has = !!(f.context_line || f.pre_context?.length)
  const [open, setOpen] = useState(start && has)
  const file = (f.file || '').replace(/^https?:\/\/[^/]+/, '')
  return (
    <div className={`frame ${f.in_app ? 'mine' : 'lib'} ${open ? 'open' : ''}`}>
      <button type="button" className="frame-head" onClick={() => has && setOpen(o => !o)} disabled={!has} aria-expanded={has ? open : undefined}>
        <span className="frame-fn mono">{f.function || '?'}</span>
        <span className="frame-file mono ellipsis">{f.module && !file.includes(f.module) ? <span className="muted">{f.module} </span> : null}{file}{f.line ? <span className="frame-ln">:{f.line}{f.col ? `:${f.col}` : ''}</span> : null}</span>
        {f.in_app && <span className="pill tiny-pill">yours</span>}
        {has && <Icon name={open ? 'chevron-up' : 'chevron-down'} size={13} className="muted" />}
      </button>
      {open && <pre className="frame-ctx">
        {(f.pre_context || []).map((l, i) => <span key={'p' + i} className="ctx-line">{l}{'\n'}</span>)}
        {f.context_line && <span className="ctx-line cur">{f.context_line}{'\n'}</span>}
        {(f.post_context || []).map((l, i) => <span key={'n' + i} className="ctx-line">{l}{'\n'}</span>)}
      </pre>}
    </div>
  )
}

function Crumbs({ list }: { list: Breadcrumb[] }) {
  const [all, setAll] = useState(false)
  const shown = all ? list : list.slice(-8)
  return (
    <Sect title="Before it happened" sub={`${list.length} steps`}>
      <ol className="crumbs">
        {shown.map((c, i) => (
          <li key={i} className={c.level === 'error' || c.level === 'fatal' ? 'bad' : ''}>
            <span className="crumb-cat">{c.category || c.type || 'step'}</span>
            <span className="crumb-msg">{c.message || Object.entries(c.data || {}).map(([k, v]) => `${k}: ${v}`).join(' · ')}</span>
            {c.at && <span className="muted tiny tnum">{new Date(c.at).toLocaleTimeString()}</span>}
          </li>
        ))}
      </ol>
      {list.length > 8 && <button type="button" className="link-btn small" onClick={() => setAll(a => !a)}>{all ? 'Only the last 8' : `All ${list.length}`}</button>}
    </Sect>
  )
}

/** The AI's answer: bold labels, lists and code, without a Markdown library. */
function Markdownish({ text }: { text: string }) {
  const blocks = text.split(/\n{2,}/)
  const inline = (s: string) => s.split(/(\*\*[^*]+\*\*|`[^`]+`)/g).map((p, i) => p.startsWith('**') ? <b key={i}>{p.slice(2, -2)}</b> : p.startsWith('`') ? <code key={i}>{p.slice(1, -1)}</code> : p)
  return (
    <div className="mdish">
      {blocks.map((b, i) => {
        if (b.startsWith('```')) return <pre key={i} className="pre-wrap small">{b.replace(/^```\w*\n?/, '').replace(/```$/, '')}</pre>
        const lines = b.split('\n')
        if (lines.every(l => /^\s*([-*]|\d+\.)\s/.test(l))) {
          const ordered = /^\s*\d+\./.test(lines[0])
          const items = lines.map((l, k) => <li key={k}>{inline(l.replace(/^\s*([-*]|\d+\.)\s/, ''))}</li>)
          return ordered ? <ol key={i}>{items}</ol> : <ul key={i}>{items}</ul>
        }
        return <p key={i}>{lines.map((l, k) => <React.Fragment key={k}>{k > 0 && <br />}{inline(l.replace(/^#+\s*/, ''))}</React.Fragment>)}</p>
      })}
    </div>
  )
}

// ---- to the other apps ----

function SendModal({ is, to, onClose, onSent }: { is: IssueView; to: 'projects' | 'tasks'; onClose: () => void; onSent: () => void }) {
  const { toast } = useToast()
  const mobile = useIsMobile()
  const [title, setTitle] = useState(`${is.project_name}: ${is.title}`.slice(0, 200))
  const [team, setTeam] = useState('')
  const [teams, setTeams] = useState<{ key: string; name: string }[] | null>(null)
  const [due, setDue] = useState('')
  const [busy, setBusy] = useState(false)
  useEffect(() => {
    if (to !== 'projects') return
    api.get<{ teams: { key: string; name: string }[]; default: string }>(`${V1}/projects-teams`).then(r => { setTeams(r.teams); setTeam(r.default || r.teams[0]?.key || '') }).catch(e => { setTeams([]); toast(errMsg(e), 'err') })
  }, [to, toast])
  const go = async (e: React.FormEvent) => {
    e.preventDefault(); setBusy(true)
    try {
      const l = await api.post<IssueLink>(`${V1}/issues/${is.id}/send`, { to, title, team, due })
      toast(to === 'projects' ? `Issue ${l.ref} made in Pocket Projects` : 'Task added in Pocket Tasks', undefined)
      announceChange(); onSent()
    } catch (err) { toast(errMsg(err), 'err') } finally { setBusy(false) }
  }
  return (
    <Modal title={to === 'projects' ? 'Make an issue in Pocket Projects' : 'Add a task in Pocket Tasks'} onClose={onClose} size="sm">
      <form onSubmit={go} className="grid" style={{ gap: 14 }}>
        <Field label="Title"><input value={title} onChange={e => setTitle(e.target.value)} required maxLength={200} autoFocus={!mobile} /></Field>
        {to === 'projects' && <Field group label="Team">{teams === null ? <Loading /> : teams.length ? <Select ariaLabel="Team" value={team} onChange={setTeam} options={teams.map(t => ({ value: t.key, label: t.name, hint: t.key }))} /> : <p className="muted small">No teams you can add issues to.</p>}</Field>}
        {to === 'tasks' && <Field label="Due (optional)"><input type="date" value={due} onChange={e => setDue(e.target.value)} /></Field>}
        <p className="muted small">It gets what broke, where, how often, the stack and a link back here.</p>
        <div className="form-actions"><button type="button" className="btn" onClick={onClose}>Cancel</button><button className="btn primary" disabled={busy || (to === 'projects' && !team)}>{busy && <Spinner />}{to === 'projects' ? 'Make the issue' : 'Add the task'}</button></div>
      </form>
    </Modal>
  )
}

