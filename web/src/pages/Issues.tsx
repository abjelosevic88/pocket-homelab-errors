// The issues: the inbox of what is broken (open), what was fixed and what
// is ignored, for every project or one, with the last 24 hours on top.
import { useCallback, useEffect, useMemo, useRef, useState } from 'react'
import { Link, useNavigate, useSearchParams } from 'react-router-dom'
import { api, V1 } from '../lib/api'
import { useApp } from '../lib/app-context'
import { announceChange, useData } from '../lib/data'
import { ago, plural } from '../lib/format'
import { hourLabels, IGNORE_FOR, LEVELS, short, splitTitle } from '../lib/errors'
import type { Issue, Status } from '../lib/types'
import { Bars, Spark } from '../components/Bars'
import { Icon } from '../components/icons'
import { Select } from '../components/Select'
import { type Anchor, Empty, Loading, Menu, PageHeader, rectOf, Segmented, Spinner, errMsg, modalOpen, useDebounce, useIsMobile, useToast } from '../components/ui'
import { openNewProject, useCrumb } from '../components/Layout'

const PAGE = 50

type StatusFilter = Status | 'all'

export default function Issues() {
  const [params, setParams] = useSearchParams()
  const navigate = useNavigate()
  const { summary, projects } = useData()
  const { settings } = useApp()
  const { toast } = useToast()
  const mobile = useIsMobile()
  const status = (params.get('status') || 'open') as StatusFilter
  const project = params.get('project') || ''
  const level = params.get('level') || ''
  const sort = params.get('sort') || 'last'
  const [q, setQ] = useState(params.get('q') || '')
  const dq = useDebounce(q.trim(), 250)
  const proj = projects?.find(p => String(p.id) === project)
  useCrumb(proj ? proj.name : undefined)

  const set = (k: string, v: string) => {
    const p = new URLSearchParams(params)
    if (v) p.set(k, v); else p.delete(k)
    setParams(p, { replace: true })
  }
  useEffect(() => { if ((params.get('q') || '') !== dq) set('q', dq) }, [dq]) // eslint-disable-line react-hooks/exhaustive-deps

  const query = useMemo(() => {
    const p = new URLSearchParams({ status, sort, limit: String(PAGE) })
    if (project) p.set('project', project)
    if (level) p.set('level', level)
    if (dq) p.set('q', dq)
    return p.toString()
  }, [status, sort, project, level, dq])

  const [list, setList] = useState<Issue[] | null>(null)
  const [total, setTotal] = useState(0)
  const [pending, setPending] = useState<{ issues: Issue[]; total: number } | null>(null)
  const [more, setMore] = useState(false)
  const [sel, setSel] = useState<Set<number>>(new Set())
  const [hi, setHi] = useState(-1)
  const [busy, setBusy] = useState(false)
  const shownQuery = useRef('')

  const load = useCallback(async (quiet: boolean) => {
    try {
      const r = await api.get<{ issues: Issue[]; total: number }>(`${V1}/issues?${query}`)
      if (quiet && shownQuery.current === query) {
        // the same rows in the same order: counts change in place; else wait for a press
        setList(cur => {
          if (cur && cur.length >= r.issues.length && r.issues.every((x, i) => cur[i]?.id === x.id)) {
            const byId = new Map(r.issues.map(x => [x.id, x]))
            setPending(null)
            return cur.map(x => byId.get(x.id) || x)
          }
          setPending(r)
          return cur
        })
        return
      }
      shownQuery.current = query
      setList(r.issues); setTotal(r.total); setPending(null); setSel(new Set()); setHi(-1)
    } catch (e) { toast(errMsg(e), 'err') }
  }, [query, toast])
  useEffect(() => { setList(null); load(false) }, [load])
  // the shared summary refreshes every half minute: look again then
  const first = useRef(true)
  useEffect(() => { if (first.current) { first.current = false; return } load(true) }, [summary]) // eslint-disable-line react-hooks/exhaustive-deps

  const showPending = () => { if (!pending) return; setList(pending.issues); setTotal(pending.total); setPending(null); setSel(new Set()) }
  const loadMore = async () => {
    if (!list) return
    setMore(true)
    try { const r = await api.get<{ issues: Issue[]; total: number }>(`${V1}/issues?${query}&offset=${list.length}`); setList([...list, ...r.issues]); setTotal(r.total) } catch (e) { toast(errMsg(e), 'err') } finally { setMore(false) }
  }

  const change = async (ids: number[], to: Status, forLen = '') => {
    if (!ids.length) return
    setBusy(true)
    try {
      await api.post(`${V1}/issues/status`, { ids, status: to, for: forLen })
      toast(`${plural(ids.length, 'issue')} ${to === 'resolved' ? 'resolved' : to === 'ignored' ? 'ignored' : 'open again'}`)
      setSel(new Set())
      announceChange()
      await load(false)
    } catch (e) { toast(errMsg(e), 'err') } finally { setBusy(false) }
  }
  const [ignoreMenu, setIgnoreMenu] = useState<Anchor | null>(null)

  // keyboard: j/k move, enter opens, x selects, e resolves
  useEffect(() => {
    const h = (e: KeyboardEvent) => {
      const t = e.target as HTMLElement
      if (e.metaKey || e.ctrlKey || e.altKey || t.closest('input, textarea, [contenteditable="true"]') || modalOpen() || document.querySelector('.menu, .sel-pop') || !list?.length) return
      if (e.key === 'j') { e.preventDefault(); setHi(i => Math.min(list.length - 1, i + 1)) }
      else if (e.key === 'k') { e.preventDefault(); setHi(i => Math.max(0, i - 1)) }
      else if (e.key === 'Enter' && hi >= 0) { e.preventDefault(); navigate(`/issues/${list[hi].id}`) }
      else if (e.key === 'x' && hi >= 0) { e.preventDefault(); toggle(list[hi].id) }
      else if (e.key === 'e' && sel.size) { e.preventDefault(); change([...sel], 'resolved') }
    }
    window.addEventListener('keydown', h)
    return () => window.removeEventListener('keydown', h)
  })
  useEffect(() => { document.querySelector(`[data-row="${hi}"]`)?.scrollIntoView({ block: 'nearest' }) }, [hi])

  const toggle = (id: number) => setSel(s => { const n = new Set(s); if (n.has(id)) n.delete(id); else n.add(id); return n })
  const allSel = !!list?.length && list.every(x => sel.has(x.id))

  const title = proj ? proj.name : { open: 'Open issues', resolved: 'Resolved', ignored: 'Ignored', all: 'All issues' }[status]
  const showOverview = status === 'open' && !project && !dq && !level
  const noProjects = projects && projects.length === 0

  return (
    <div className="page issues-page">
      <PageHeader title={title} sub={list ? `${plural(total, 'issue')}${proj?.container ? ` · container ${proj.container}` : ''}` : ' '}
        actions={proj && <Link className="btn sm" to={`/projects/${proj.id}`}><Icon name="settings" size={14} />Project</Link>} />

      {showOverview && summary && !noProjects && <Overview />}

      <div className="toolbar issues-toolbar">
        <div className="search-in input-icon"><Icon name="search" size={15} /><input value={q} onChange={e => setQ(e.target.value)} placeholder="Find an error, a file, a project…" aria-label="Find" /></div>
        <Segmented size="sm" label="Status" value={status} onChange={v => set('status', v === 'open' ? '' : v)} options={[{ value: 'open', label: 'Open' }, { value: 'resolved', label: 'Resolved' }, { value: 'ignored', label: 'Ignored' }, { value: 'all', label: 'All' }]} />
        <Select ariaLabel="Project" value={project} onChange={v => set('project', v)} options={[{ value: '', label: 'Every project' }, ...(projects || []).map(p => ({ value: String(p.id), label: p.name, hint: p.archived ? 'archived' : undefined }))]} />
        {!mobile && <Select ariaLabel="Level" value={level} onChange={v => set('level', v)} options={[{ value: '', label: 'Every level' }, { value: 'fatal', label: 'Fatal only' }, { value: 'error', label: 'Errors and worse' }, { value: 'warning', label: 'Warnings and worse' }]} />}
        {!mobile && <Select ariaLabel="Order" value={sort} onChange={v => set('sort', v === 'last' ? '' : v)} options={[{ value: 'last', label: 'Last seen' }, { value: 'first', label: 'First seen' }, { value: 'times', label: 'Most often' }, { value: 'people', label: 'Most people' }]} />}
      </div>

      <div className="bulk-bar">
        <label className="check-row" title="Select all shown"><input type="checkbox" checked={allSel} disabled={!list?.length} onChange={() => setSel(allSel ? new Set() : new Set(list!.map(x => x.id)))} />{sel.size ? `${sel.size} selected` : 'Select'}</label>
        <span className="spacer" />
        <button type="button" className="btn sm" title="Resolve" disabled={!sel.size || busy || status === 'resolved'} onClick={() => change([...sel], 'resolved')}><Icon name="check" size={14} /><span className="hide-sm">Resolve</span></button>
        <button type="button" className="btn sm" title="Ignore" disabled={!sel.size || busy || status === 'ignored'} onClick={e => setIgnoreMenu(rectOf(e.currentTarget))}><Icon name="eye-off" size={14} /><span className="hide-sm">Ignore</span></button>
        <button type="button" className="btn sm" title="Open again" disabled={!sel.size || busy || status === 'open'} onClick={() => change([...sel], 'open')}><Icon name="undo" size={14} /><span className="hide-sm">Open again</span></button>
        <button type="button" className={`btn sm ${pending ? 'primary' : 'ghost'}`} disabled={!pending} onClick={showPending} title={pending ? 'New errors came in' : 'Up to date'}><Icon name="refresh" size={14} /><span className="hide-sm">{pending ? 'Show new' : 'Up to date'}</span></button>
      </div>
      {ignoreMenu && <Menu anchor={ignoreMenu} onClose={() => setIgnoreMenu(null)} items={IGNORE_FOR.map(o => ({ label: o.label, onSelect: () => change([...sel], 'ignored', o.value) }))} />}

      {!list ? <Loading /> : noProjects ? <FirstSteps /> : !list.length ? (
        <Empty icon={status === 'open' ? 'check-circle' : 'inbox'} title={status === 'open' && !dq ? 'Nothing is broken' : 'Nothing here'}>
          {status === 'open' && !dq ? (proj ? `${proj.name} has no open issues.` : 'No open issues in any project. New errors show up here as they happen.') : 'No issues match.'}
        </Empty>
      ) : (
        <div className="card flush">
          <ul className="issue-list">
            {list.map((is, i) => <IssueRow key={is.id} is={is} i={i} hi={i === hi} selected={sel.has(is.id)} onToggle={() => toggle(is.id)} showProject={!project} mobile={mobile} />)}
          </ul>
          {list.length < total && <div className="list-more"><button type="button" className="btn sm" onClick={loadMore} disabled={more}>{more ? <Spinner /> : null}Show more ({total - list.length})</button></div>}
        </div>
      )}
      {settings?.demo && <p className="muted small center">Demo data: made up, nothing here is real.</p>}
    </div>
  )
}

function IssueRow({ is, i, hi, selected, onToggle, showProject, mobile }: { is: Issue; i: number; hi: boolean; selected: boolean; onToggle: () => void; showProject: boolean; mobile: boolean }) {
  const [type, msg] = splitTitle(is.title, is.type)
  return (
    <li data-row={i} className={`issue-row lv-${is.level} ${hi ? 'hi' : ''} ${selected ? 'sel' : ''}`}>
      <input type="checkbox" className="issue-check" checked={selected} onChange={onToggle} aria-label="Select" />
      <Link to={`/issues/${is.id}`} className="issue-main">
        <span className="issue-title">
          <span className={`lv-dot lv-${is.level}`} title={LEVELS[is.level]?.label} />
          {type && <b className="issue-type">{type}</b>}
          <span className="issue-msg ellipsis">{msg || (type ? '' : is.title)}</span>
        </span>
        <span className="issue-sub ellipsis">
          {is.new && is.status === 'open' && <span className="pill st-new">New</span>}
          {is.regressed && is.status === 'open' && <span className="pill st-back"><Icon name="rotate-ccw" size={11} />Back</span>}
          {is.status === 'resolved' && <span className="pill st-done"><Icon name="check" size={11} />Resolved</span>}
          {is.status === 'ignored' && <span className="pill"><Icon name="eye-off" size={11} />Ignored</span>}
          {showProject && <span className="issue-proj">{is.project_name}</span>}
          {is.culprit && <span className="mono issue-culprit">{is.culprit}</span>}
        </span>
      </Link>
      {!mobile && <Spark values={is.hours} label={`${is.events_24h} in the last 24 hours`} />}
      <span className="issue-nums">
        <span className="tnum" title={`${is.times.toLocaleString()} times`}><Icon name="activity" size={12} />{short(is.times)}</span>
        {is.people > 0 && <span className="tnum" title={`${is.people} people`}><Icon name="users" size={12} />{short(is.people)}</span>}
        <span className="muted" title={`Last seen ${new Date(is.last_seen).toLocaleString()}`}>{ago(is.last_seen)}</span>
      </span>
    </li>
  )
}

function Overview() {
  const { summary } = useData()
  const { settings } = useApp()
  const { toast } = useToast()
  const [digest, setDigest] = useState<string | null>(null)
  const [busy, setBusy] = useState(false)
  const o = summary!.overview
  const labels = useMemo(() => hourLabels(24), [summary]) // eslint-disable-line react-hooks/exhaustive-deps
  const w = summary!.watch
  const sumUp = async () => {
    setBusy(true)
    try { setDigest((await api.post<{ text: string }>(`${V1}/ai/digest`, { range: '24h' })).text) } catch (e) { toast(errMsg(e), 'err') } finally { setBusy(false) }
  }
  return (
    <section className="card overview">
      <div className="ov-stats">
        <div className="ov-stat"><span className="ov-n tnum">{o.open.toLocaleString()}</span><span className="ov-l">open</span></div>
        <div className="ov-stat"><span className={`ov-n tnum ${o.new ? 'warn-text' : ''}`}>{o.new.toLocaleString()}</span><span className="ov-l">new today</span></div>
        <div className="ov-stat"><span className={`ov-n tnum ${o.regressed ? 'warn-text' : ''}`}>{o.regressed.toLocaleString()}</span><span className="ov-l">came back</span></div>
        <div className="ov-stat"><span className="ov-n tnum">{short(o.events_24h)}</span><span className="ov-l">errors in 24 h</span></div>
        <span className="spacer" />
        {settings?.ai_help && <button type="button" className="btn sm" onClick={sumUp} disabled={busy} title="Pocket AI sums up the last 24 hours">{busy ? <Spinner /> : <Icon name="sparkles" size={14} />}Sum up</button>}
      </div>
      <Bars labels={labels} series={[
        { key: 'fatal', label: 'Fatal', values: o.hours.fatal || [], className: 'b-fatal' },
        { key: 'error', label: 'Errors', values: o.hours.error || [], className: 'b-error' },
        { key: 'warning', label: 'Warnings', values: o.hours.warning || [], className: 'b-warning' },
        { key: 'info', label: 'Info', values: o.hours.info || [], className: 'b-info' },
      ]} height={84} />
      {digest && <div className="ai-box"><Icon name="sparkles" size={14} /><p>{digest}</p><button type="button" className="btn ghost icon-only sm" aria-label="Close" onClick={() => setDigest(null)}><Icon name="x" size={14} /></button></div>}
      {w.on && <p className="muted small ov-watch"><Icon name="package" size={13} />{w.error ? <span className="warn-text">{w.error}</span> : <>Reading {plural(w.containers.length, 'container')} on this box{w.checked_at ? `, last looked ${ago(w.checked_at)}` : ''}.</>} <Link to="/settings/box">Change</Link></p>}
    </section>
  )
}

function FirstSteps() {
  const { settings } = useApp()
  return (
    <div className="first-steps">
      <Empty icon="bug" title="Catch your first error">
        Make a project for a website or an app and paste one line into it, or let Errors read the logs of this box’s containers — every Pocket app included — with nothing to install.
      </Empty>
      <div className="row center wrap">
        <button type="button" className="btn primary" onClick={openNewProject}><Icon name="plus" size={15} />New project</button>
        {settings?.is_admin && <Link className="btn" to="/settings/box"><Icon name="package" size={15} />Read this box’s containers</Link>}
      </div>
    </div>
  )
}
