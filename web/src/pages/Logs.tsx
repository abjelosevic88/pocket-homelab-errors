// Logs: every line this box's containers and services write. Overview
// (how much, how bad, from where, what repeats), Explore (search and read
// the lines, live), Patterns, Web traffic (from Caddy) and Sources (what is
// kept of each). Admins only: the lines can say anything an app wrote.
import { Fragment, useCallback, useEffect, useMemo, useRef, useState } from 'react'
import { Link, useNavigate, useParams, useSearchParams } from 'react-router-dom'
import { useApp } from '../lib/app-context'
import {
  delta, fmtBytes, fmtCount, fmtMs, groupOf, GROUPS, KEEPS, lineTime, LOG_LEVELS, logsApi, RANGES, searchTerms, STACK,
  type Histogram, type Keep, type LogLevel, type LogLine, type LogStatus, type Overview, type Pattern, type RangeId, type SourceCount, type Web,
} from '../lib/logs'
import { Heatmap, LineChart, Meter, MiniBars, ShareBar, StackChart, type StackSeries } from '../components/LogCharts'
import { Icon } from '../components/icons'
import { Select } from '../components/Select'
import { Card, Empty, Loading, PageHeader, Segmented, Spinner, Switch, errMsg, useDebounce, useToast } from '../components/ui'
import { useCrumb } from '../components/Layout'

type Tab = 'overview' | 'explore' | 'patterns' | 'web' | 'sources'
const TABS: { id: Tab; label: string; icon: string }[] = [
  { id: 'overview', label: 'Overview', icon: 'chart' },
  { id: 'explore', label: 'Explore', icon: 'search' },
  { id: 'patterns', label: 'Patterns', icon: 'repeat' },
  { id: 'web', label: 'Web traffic', icon: 'globe' },
  { id: 'sources', label: 'Sources', icon: 'server' },
]

const LEVEL_LABEL: Record<LogLevel, string> = { fatal: 'Fatal', error: 'Errors', warning: 'Warnings', info: 'Info', debug: 'Debug' }

function levelSeries(h: Histogram): StackSeries[] {
  return STACK.map(l => ({ key: l, label: LEVEL_LABEL[l], cls: `lv-${l}`, values: h.levels[l] || [] })).filter(s => s.values.some(v => v))
}

function agoMs(ms: number): string {
  if (!ms) return 'never'
  const s = Math.max(0, (Date.now() - ms) / 1000)
  if (s < 45) return 'just now'
  if (s < 3600) return `${Math.max(1, Math.round(s / 60))} min ago`
  if (s < 86400) return `${Math.round(s / 3600)} h ago`
  return `${Math.round(s / 86400)} d ago`
}

export default function Logs() {
  const { tab: raw } = useParams()
  const tab: Tab = TABS.find(t => t.id === raw)?.id || 'overview'
  const navigate = useNavigate()
  const [params] = useSearchParams()
  const { user } = useApp()
  const [status, setStatus] = useState<LogStatus | null>(null)
  const [err, setErr] = useState('')
  useCrumb(tab === 'overview' ? 'Logs' : `Logs · ${TABS.find(t => t.id === tab)!.label}`)
  const loadStatus = useCallback(() => logsApi.status().then(setStatus).catch(e => setErr(errMsg(e))), [])
  useEffect(() => { loadStatus() }, [loadStatus])
  useEffect(() => { const t = setInterval(loadStatus, 30000); return () => clearInterval(t) }, [loadStatus])
  const go = (t: Tab, q?: URLSearchParams) => {
    const keep = new URLSearchParams()
    const r = params.get('r'); if (r) keep.set('r', r)
    navigate(`/logs${t === 'overview' ? '' : '/' + t}${q ? '?' + q.toString() : keep.toString() ? '?' + keep.toString() : ''}`)
  }

  if (user && user.role !== 'admin') return <div className="page"><Empty title="Only admins read the logs" icon="lock">The lines can say anything an app wrote, so only this box’s admins see them.</Empty></div>
  if (err) return <div className="page"><Empty title="The logs did not open" icon="alert">{err}</Empty></div>
  if (!status) return <div className="page"><Loading /></div>
  if (!status.settings.on) return <div className="page"><TurnOn status={status} onDone={loadStatus} /></div>

  return (
    <div className="page logs-page">
      <PageHeader title="Logs" sub={<StatusLine status={status} />} actions={<Link to="/settings/box" className="btn ghost"><Icon name="settings" size={15} />Logs settings</Link>} />
      <nav className="logs-tabs" role="tablist" aria-label="Logs">
        {TABS.map(t => (
          <button key={t.id} type="button" role="tab" aria-selected={t.id === tab} className={t.id === tab ? 'active' : ''} onClick={() => go(t.id)}>
            <Icon name={t.icon} size={15} />{t.label}
          </button>
        ))}
      </nav>
      {tab === 'overview' && <OverviewTab status={status} go={go} />}
      {tab === 'explore' && <ExploreTab />}
      {tab === 'patterns' && <PatternsTab go={go} />}
      {tab === 'web' && <WebTab go={go} />}
      {tab === 'sources' && <SourcesTab go={go} />}
    </div>
  )
}

function StatusLine({ status }: { status: LogStatus }) {
  const j = status.journal
  return (
    <span className="logs-status">
      <span className={`dot-live ${status.docker.error ? 'bad' : ''}`} aria-hidden="true" />
      {status.docker.error ? status.docker.error : <>{status.docker.containers} containers</>}
      <span className="sep">·</span>
      {j.running ? <>the journal</> : <span className="muted" title={j.error}>no journal{j.error ? ` (${j.error.split(':')[0]})` : ''}</span>}
      <span className="sep">·</span>
      {fmtCount(status.storage.lines)} lines kept, {fmtBytes(status.storage.used)}
    </span>
  )
}

function TurnOn({ status, onDone }: { status: LogStatus; onDone: () => void }) {
  const { toast } = useToast()
  const [busy, setBusy] = useState(false)
  return (
    <div className="logs-intro">
      <div className="logs-intro-art" aria-hidden="true"><Icon name="list" size={30} /></div>
      <h1>Every line your box writes, in one place</h1>
      <p className="muted">Errors already reads your containers for what broke. Switch this on and it keeps <b>every</b> line too — every container’s and, when the journal is mounted, your system and user services’ (Pocket AI included) — to search, follow live and chart: how much each app talks, when errors pile up, which messages repeat, and how your websites answer.</p>
      <ul className="logs-intro-list">
        <li><Icon name="search" size={15} />Search every line of the last {status.settings.keep_days} days, live as they come</li>
        <li><Icon name="chart" size={15} />Volume, levels and a heat map of who is noisy when</li>
        <li><Icon name="repeat" size={15} />The messages that repeat, and new ones the moment they appear</li>
        <li><Icon name="globe" size={15} />Web traffic from Caddy: requests, failures and answer times per site</li>
      </ul>
      {!status.docker.available && <p className="warn-text small">The Docker socket is not mounted, so only the journal can be read.</p>}
      <button type="button" className="btn primary" disabled={busy} onClick={async () => {
        setBusy(true)
        try { await logsApi.saveSettings({ on: true }); toast('Logs are on — the first lines arrive within ten seconds'); onDone() } catch (e) { toast(errMsg(e), 'err') } finally { setBusy(false) }
      }}>{busy ? <Spinner /> : <Icon name="zap" size={15} />}Keep this box’s logs</button>
      <p className="muted tiny">Up to {fmtBytes(status.settings.max_mb * 1048576)} on disk, oldest lines go first. Change it in Settings → This box.</p>
    </div>
  )
}

function useRange(def: RangeId = '24h'): [RangeId, (r: RangeId) => void] {
  const [params, setParams] = useSearchParams()
  const r = (RANGES.find(x => x.value === params.get('r'))?.value || def) as RangeId
  const set = (v: RangeId) => { const p = new URLSearchParams(params); p.set('r', v); setParams(p, { replace: true }) }
  return [r, set]
}

function RangePicker({ value, onChange }: { value: RangeId; onChange: (r: RangeId) => void }) {
  return <Segmented size="sm" label="Time span" options={RANGES.map(r => ({ value: r.value, label: r.label }))} value={value} onChange={onChange} />
}

function exploreQuery(o: { source?: number | string; q?: string; level?: string; from?: number; to?: number; pattern?: string; group?: string; r?: string }) {
  const p = new URLSearchParams()
  for (const [k, v] of Object.entries(o)) if (v !== undefined && v !== '' && v !== 0) p.set(k, String(v))
  return p
}

// ---------------- Overview ----------------

function OverviewTab({ status, go }: { status: LogStatus; go: (t: Tab, q?: URLSearchParams) => void }) {
  const [range, setRange] = useRange()
  const [ov, setOv] = useState<Overview | null>(null)
  const [pats, setPats] = useState<Pattern[] | null>(null)
  const [fresh, setFresh] = useState<Pattern[] | null>(null)
  const [web, setWeb] = useState<Web | null>(null)
  const [heat, setHeat] = useState<'errors' | 'lines'>('errors')
  const [only, setOnly] = useState<'all' | 'bad'>('all')
  const { toast } = useToast()
  const load = useCallback(() => {
    const q = `range=${range}`
    logsApi.overview(q).then(setOv).catch(e => toast(errMsg(e), 'err'))
    logsApi.patterns(`${q}&limit=8&sort=errors`).then(r => setPats(r.patterns)).catch(() => setPats([]))
    logsApi.patterns(`${q}&limit=6&sort=new`).then(r => setFresh(r.patterns)).catch(() => setFresh([]))
    logsApi.web(q).then(setWeb).catch(() => setWeb(null))
  }, [range, toast])
  useEffect(() => { load() }, [load])
  useEffect(() => { const t = setInterval(load, 60000); return () => clearInterval(t) }, [load])
  if (!ov) return <Loading />
  const t = ov.totals, p = ov.previous
  const bad = (t.error || 0) + (t.fatal || 0)
  const badBefore = (p.error || 0) + (p.fatal || 0)
  const d = delta(t.all || 0, p.all || 0)
  const de = delta(bad, badBefore)
  const busiest = ov.sources[0]
  const worst = [...ov.sources].sort((a, b) => b.errors - a.errors)[0]
  const rangeLabel = RANGES.find(r => r.value === range)!.label
  return (
    <div className="logs-grid">
      <div className="logs-bar"><RangePicker value={range} onChange={setRange} /><span className="muted small">Counts include lines a source only counts (Sources → what is kept).</span></div>
      <div className="kpis">
        <Kpi icon="list" label={`Lines, last ${rangeLabel}`} value={fmtCount(t.all || 0)} delta={d} sub={`${fmtBytes(ov.bytes)} written`} onClick={() => go('explore', exploreQuery({ r: range }))} />
        <Kpi icon="alert" tone={bad ? 'bad' : ''} label="Errors" value={fmtCount(bad)} delta={de} invert sub={t.all ? `${((bad / (t.all || 1)) * 100).toFixed(bad / (t.all || 1) < .01 ? 2 : 1)}% of lines` : ''} onClick={() => go('explore', exploreQuery({ level: 'error', r: range }))} />
        <Kpi icon="flag" tone={t.warning ? 'warn' : ''} label="Warnings" value={fmtCount(t.warning || 0)} delta={delta(t.warning || 0, p.warning || 0)} invert onClick={() => go('explore', exploreQuery({ level: 'warning', r: range }))} />
        <Kpi icon="activity" label="Busiest" value={busiest ? busiest.label : '—'} small sub={busiest ? `${fmtCount(busiest.lines)} lines · ${Math.round(busiest.lines / (t.all || 1) * 100)}%` : ''} onClick={busiest ? () => go('explore', exploreQuery({ source: busiest.id, r: range })) : undefined} />
        <Kpi icon="bug" tone={worst?.errors ? 'bad' : ''} label="Most errors" value={worst?.errors ? worst.label : 'None'} small sub={worst?.errors ? `${fmtCount(worst.errors)} errors` : 'Nothing went wrong'} onClick={worst?.errors ? () => go('explore', exploreQuery({ source: worst.id, level: 'error', r: range })) : undefined} />
        <div className="kpi">
          <div className="kpi-head"><Icon name="database" size={14} />Kept on disk</div>
          <div className="kpi-val sm">{fmtBytes(status.storage.used)}</div>
          <Meter value={status.storage.used} max={status.storage.max} label="Logs storage" text={`of ${fmtBytes(status.storage.max)} · since ${status.storage.oldest ? agoMs(status.storage.oldest).replace(' ago', '') + ' ago' : '—'}`} />
        </div>
      </div>

      <Card title="Lines over time" sub="By level. Drag across the bars to read that span." className="span-2"
        actions={<Segmented size="sm" label="Which lines" options={[{ value: 'all', label: 'All lines' }, { value: 'bad', label: 'Problems only' }]} value={only} onChange={v => setOnly(v as 'all' | 'bad')} />}>
        <StackChart label="Lines per level over time" series={levelSeries(ov.volume).filter(s => only === 'all' || ['fatal', 'error', 'warning'].includes(s.key))} from={ov.volume.from} bucket={ov.volume.bucket} height={170}
          onSelect={(from, to) => go('explore', exploreQuery({ from, to }))}
          onPick={at => go('explore', exploreQuery({ from: at, to: at + ov.volume.bucket }))} />
      </Card>

      <Card title="Where they come from" sub={`${ov.sources.length} sources wrote in the last ${rangeLabel}`}>
        <ShareBar parts={GROUPS.map((g, i) => ({ key: g.id, label: g.label, cls: `g${i + 1}`, value: (ov.groups[g.id] || []).reduce((a, b) => a + b, 0) }))} onPick={g => go('explore', exploreQuery({ group: g, r: range }))} />
        <ol className="top-sources">
          {ov.sources.slice(0, 8).map(s => {
            const gi = GROUPS.findIndex(g => g.id === s.group) + 1
            return (
              <li key={s.id}>
                <button type="button" onClick={() => go('explore', exploreQuery({ source: s.id, r: range }))}>
                  <span className={`gdot g${gi}`} aria-hidden="true" />
                  <span className="ts-name ellipsis" title={s.name}>{s.label}</span>
                  <MiniBars values={s.spark} label={`${s.label}: lines over time`} width={84} height={18} />
                  <span className="ts-n tnum">{fmtCount(s.lines)}</span>
                  <span className={`ts-e tnum ${s.errors ? 'bad' : 'muted'}`} title="Errors">{s.errors ? fmtCount(s.errors) : '—'}</span>
                </button>
              </li>
            )
          })}
        </ol>
        <button type="button" className="btn ghost sm" onClick={() => go('sources')}>All sources<Icon name="chevron-right" size={14} /></button>
      </Card>

      <Card title="When and where it went wrong" sub={heat === 'errors' ? 'Errors per source. Darker is more.' : 'Lines per source. Darker is more.'}
        actions={<Segmented size="sm" label="Show" options={[{ value: 'errors', label: 'Errors' }, { value: 'lines', label: 'Lines' }]} value={heat} onChange={v => setHeat(v as 'errors' | 'lines')} />}>
        {ov.heat.sources.length ? (
          <Heatmap rows={ov.heat.sources.map((id, i) => ({ id, label: ov.heat.labels[i] }))} values={heat === 'errors' ? ov.heat.errors : ov.heat.lines}
            from={ov.heat.from} bucket={ov.heat.bucket} mode={heat}
            onPick={(src, from, to) => go('explore', exploreQuery({ source: src, from, to, level: heat === 'errors' ? 'error' : '' }))} />
        ) : <p className="muted">Nothing yet.</p>}
      </Card>

      <Card title="Problems that repeat" sub="Warnings and errors, the same message with other values counted as one" actions={<button type="button" className="btn ghost sm" onClick={() => go('patterns')}>All<Icon name="chevron-right" size={14} /></button>}>
        <PatternList list={pats} compact go={go} empty="No warnings or errors repeat. Quiet." />
      </Card>

      <Card title="New messages" sub={`First seen in the last ${rangeLabel}: what changed`} actions={<button type="button" className="btn ghost sm" onClick={() => go('patterns', exploreQuery({ sort: 'new' } as never))}>All<Icon name="chevron-right" size={14} /></button>}>
        <PatternList list={fresh} compact go={go} empty="Nothing new: every message was seen before." />
      </Card>

      {web && web.total > 0 && (
        <Card title="Web traffic" sub="What Caddy answered" className="span-2" actions={<button type="button" className="btn ghost sm" onClick={() => go('web')}>Details<Icon name="chevron-right" size={14} /></button>}>
          <div className="web-mini">
            <div className="web-mini-stats">
              <div><b className="tnum">{fmtCount(web.total)}</b><span>requests</span></div>
              <div className={web.errors ? 'bad' : ''}><b className="tnum">{web.errors ? ((web.errors / web.total) * 100).toFixed(2) + '%' : '0%'}</b><span>failed (5xx)</span></div>
              <div><b className="tnum">{fmtMs(web.avg_ms)}</b><span>on average</span></div>
              <div><b className="tnum">{fmtCount(web.slow)}</b><span>over a second</span></div>
            </div>
            <StackChart label="Requests by answer over time" height={110} series={webSeries(web)} from={web.from} bucket={web.bucket} unit="requests" />
          </div>
        </Card>
      )}
    </div>
  )
}

function Kpi({ icon, label, value, sub, delta: d, invert, tone = '', small, onClick }: { icon: string; label: string; value: string; sub?: string; delta?: { text: string; dir: string }; invert?: boolean; tone?: string; small?: boolean; onClick?: () => void }) {
  const good = d && (invert ? d.dir === 'down' : false)
  const worse = d && invert && d.dir === 'up'
  const Tag = onClick ? 'button' : 'div'
  return (
    <Tag type={onClick ? 'button' : undefined} className={`kpi ${tone} ${onClick ? 'click' : ''}`} onClick={onClick}>
      <div className="kpi-head"><Icon name={icon} size={14} />{label}</div>
      <div className={`kpi-val ${small ? 'sm ellipsis' : ''}`} title={small ? value : undefined}>{value}</div>
      <div className="kpi-sub">
        {d && d.text !== '—' && <span className={`kpi-delta ${good ? 'good' : worse ? 'worse' : ''}`}>{d.dir === 'up' ? <Icon name="trending-up" size={12} /> : d.dir === 'down' ? <Icon name="trending-down" size={12} /> : null}{d.text}</span>}
        {sub && <span className="muted">{sub}</span>}
      </div>
    </Tag>
  )
}

function Template({ t, terms = [] }: { t: string; terms?: string[] }) {
  const parts = t.split(/(‹[^›]*›)/)
  return <>{parts.map((p, i) => p.startsWith('‹') ? <span key={i} className="tpl-var">{p.slice(1, -1)}</span> : <Mark key={i} text={p} terms={terms} />)}</>
}

function PatternList({ list, compact, go, empty }: { list: Pattern[] | null; compact?: boolean; go: (t: Tab, q?: URLSearchParams) => void; empty: string }) {
  if (!list) return <Loading />
  if (!list.length) return <p className="muted small">{empty}</p>
  const max = Math.max(...list.map(p => p.n))
  return (
    <ol className={`pat-list ${compact ? 'compact' : ''}`}>
      {list.map(p => (
        <li key={p.id}>
          <button type="button" onClick={() => go('explore', exploreQuery({ pattern: p.id, source: p.source_id }))}>
            <LevelTag level={p.level} />
            <span className="pat-main">
              <span className="pat-tpl"><Template t={p.template} /></span>
              <span className="pat-sub muted">{p.source_label}{p.new && <span className="pill st-new tiny-pill">new</span>}{!compact && <> · first {agoMs(p.first_at)} · last {agoMs(p.last_at)}</>}</span>
            </span>
            {!compact && <MiniBars values={p.spark} label="Over time" width={110} height={22} />}
            <span className="pat-n">
              <b className="tnum">{fmtCount(p.n)}</b>
              <span className="pat-meter" aria-hidden="true"><i style={{ width: `${(p.n / max) * 100}%` }} className={`lv-${p.level}`} /></span>
            </span>
          </button>
        </li>
      ))}
    </ol>
  )
}

function LevelTag({ level }: { level: LogLevel }) {
  const l = LOG_LEVELS.find(x => x.id === level)!
  return <span className={`lvtag lv-${level}`} title={l.label}>{l.short}</span>
}

function Mark({ text, terms }: { text: string; terms: string[] }) {
  if (!terms.length || !text) return <>{text}</>
  const re = new RegExp(`(${terms.map(t => t.replace(/[.*+?^${}()|[\]\\]/g, '\\$&')).join('|')})`, 'gi')
  const parts = text.split(re)
  return <>{parts.map((p, i) => i % 2 ? <mark key={i}>{p}</mark> : <Fragment key={i}>{p}</Fragment>)}</>
}

// ---------------- Explore ----------------

const LEVEL_FILTERS = [{ value: '', label: 'All' }, { value: 'warning', label: 'Warn+' }, { value: 'error', label: 'Errors' }]

function ExploreTab() {
  const [params, setParams] = useSearchParams()
  const { toast } = useToast()
  const [q, setQ] = useState(params.get('q') || '')
  const dq = useDebounce(q.trim(), 300)
  const source = params.get('source') || ''
  const group = params.get('group') || ''
  const level = params.get('level') || ''
  const pattern = params.get('pattern') || ''
  const from = Number(params.get('from')) || 0
  const to = Number(params.get('to')) || 0
  const range = (RANGES.find(x => x.value === params.get('r'))?.value || '24h') as RangeId
  const [live, setLive] = useState(false)
  const [sources, setSources] = useState<SourceCount[]>([])
  const [hist, setHist] = useState<Histogram | null>(null)
  const [lines, setLines] = useState<LogLine[] | null>(null)
  const [more, setMore] = useState(false)
  const [busy, setBusy] = useState(false)
  const [open, setOpen] = useState<number | null>(null)
  const [fresh, setFresh] = useState<Set<number>>(new Set())
  const set = (k: string, v: string) => { const p = new URLSearchParams(params); if (v) p.set(k, v); else p.delete(k); setParams(p, { replace: true }) }
  useEffect(() => { if ((params.get('q') || '') !== dq) set('q', dq) }, [dq]) // eslint-disable-line react-hooks/exhaustive-deps
  useEffect(() => { logsApi.sources('range=24h').then(r => setSources(r.sources)).catch(() => {}) }, [])

  const query = useMemo(() => {
    const p = new URLSearchParams()
    if (dq) p.set('q', dq)
    if (source) p.set('source', source)
    if (group) p.set('group', group)
    if (level) p.set('level', level)
    if (pattern) p.set('pattern', pattern)
    if (from) { p.set('from', String(from)); p.set('to', String(to || Date.now())) } else p.set('range', range)
    return p
  }, [dq, source, group, level, pattern, from, to, range])
  const qs = query.toString()

  useEffect(() => {
    let gone = false
    setLines(null); setOpen(null)
    logsApi.lines(`${qs}&limit=200`).then(r => { if (!gone) { setLines(r.lines); setMore(r.more) } }).catch(e => { toast(errMsg(e), 'err'); setLines([]) })
    logsApi.histogram(qs).then(h => { if (!gone) setHist(h) }).catch(() => setHist(null))
    return () => { gone = true }
  }, [qs, toast])

  // live: the newest lines every three seconds, on top (only without an end time)
  const newest = lines?.[0]?.id || 0
  const newestRef = useRef(newest)
  newestRef.current = newest
  useEffect(() => {
    if (!live || to) return
    const t = setInterval(async () => {
      try {
        const p = new URLSearchParams(query); p.delete('range'); p.delete('from'); p.delete('to')
        p.set('after', String(newestRef.current)); p.set('limit', '500')
        const r = await logsApi.lines(p.toString())
        if (r.lines.length) {
          setFresh(new Set(r.lines.map(l => l.id)))
          setLines(cur => [...r.lines, ...(cur || [])].slice(0, 2000))
        }
      } catch { /* the next tick tries again */ }
    }, 3000)
    return () => clearInterval(t)
  }, [live, to, query])

  const older = async () => {
    if (!lines?.length) return
    setBusy(true)
    try {
      const r = await logsApi.lines(`${qs}&limit=200&before=${lines[lines.length - 1].id}`)
      setLines([...lines, ...r.lines]); setMore(r.more)
    } catch (e) { toast(errMsg(e), 'err') } finally { setBusy(false) }
  }

  const srcOptions = useMemo(() => [
    ...GROUPS.map(g => ({ value: 'g:' + g.id, label: g.label, group: 'Groups' })),
    ...sources.slice().sort((a, b) => a.label.localeCompare(b.label)).map(s => ({ value: String(s.id), label: s.label, hint: s.lines ? fmtCount(s.lines) : '', group: groupOf(s.group).label })),
  ], [sources])
  const srcValue = group ? 'g:' + group : source
  const terms = searchTerms(dq)
  const patternLine = pattern && lines?.find(l => l.pattern === pattern)

  return (
    <div className="explore">
      <div className="explore-bar">
        <div className="input-icon explore-q">
          <Icon name="search" size={15} />
          <input type="search" value={q} onChange={e => setQ(e.target.value)} placeholder="Search the lines — words, “a phrase”, -not" aria-label="Search the lines" autoFocus />
        </div>
        <Select className="explore-src" value={srcValue} options={srcOptions} placeholder="Every source" ariaLabel="Source" clearLabel="Every source"
          onChange={v => { const p = new URLSearchParams(params); p.delete('source'); p.delete('group'); if (v.startsWith('g:')) p.set('group', v.slice(2)); else if (v) p.set('source', v); setParams(p, { replace: true }) }} />
        <Segmented size="sm" label="Level" options={LEVEL_FILTERS} value={level} onChange={v => set('level', v)} />
        {from ? (
          <span className="chip">
            <Icon name="clock" size={13} />{new Date(from).toLocaleString(undefined, { day: 'numeric', month: 'short', hour: '2-digit', minute: '2-digit' })} – {new Date(to || Date.now()).toLocaleTimeString(undefined, { hour: '2-digit', minute: '2-digit' })}
            <button type="button" aria-label="Back to the whole span" onClick={() => { const p = new URLSearchParams(params); p.delete('from'); p.delete('to'); setParams(p, { replace: true }) }}><Icon name="x" size={12} /></button>
          </span>
        ) : <Segmented size="sm" label="Time span" options={RANGES.map(r => ({ value: r.value, label: r.label }))} value={range} onChange={v => set('r', v)} />}
        <span className="grow" />
        <label className={`live-toggle ${live && !to ? 'on' : ''}`} title={to ? 'Live needs a span that ends now' : 'Show new lines as they come'}>
          <Switch size="sm" checked={live && !to} disabled={!!to} onChange={setLive} label="Live" />Live
        </label>
        <a className="btn ghost sm" href={`/api/v1/logs/export.txt?${qs}`} download><Icon name="download" size={14} />Export</a>
      </div>
      {pattern && (
        <div className="explore-note">
          <Icon name="repeat" size={14} />Only lines with the same message{patternLine ? <> as <code className="ellipsis">{patternLine.msg.slice(0, 80)}</code></> : ''}
          <button type="button" className="btn ghost sm" onClick={() => set('pattern', '')}>Show all</button>
        </div>
      )}
      <Card flush className="explore-hist">
        {hist ? <StackChart label="Matching lines over time" series={levelSeries(hist)} from={hist.from} bucket={hist.bucket} height={86} legend={hist.total > 0}
          onSelect={(f, t) => { const p = new URLSearchParams(params); p.set('from', String(f)); p.set('to', String(t)); setParams(p, { replace: true }) }} /> : <div className="chart-ph" />}
        <div className="explore-count muted small">{hist ? <>{hist.total.toLocaleString()} {hist.total === 1 ? 'line' : 'lines'} kept match</> : ' '}</div>
      </Card>
      <div className="log-view" role="log" aria-live={live ? 'polite' : 'off'}>
        {!lines ? <Loading /> : !lines.length ? (
          <Empty title="No lines match" icon="search">{dq ? 'Try fewer words, or a longer span.' : 'Nothing was kept for this yet. A source set to “Count only” keeps no lines.'}</Empty>
        ) : lines.map((l, i) => {
          const showDay = i === 0 || new Date(lines[i - 1].at).toDateString() !== new Date(l.at).toDateString()
          return (
            <Fragment key={l.id}>
              {showDay && <div className="log-day">{new Date(l.at).toLocaleDateString(undefined, { weekday: 'long', day: 'numeric', month: 'long' })}</div>}
              <LogRow line={l} terms={terms} open={open === l.id} fresh={fresh.has(l.id)} onToggle={() => setOpen(open === l.id ? null : l.id)}
                onSource={() => { const p = new URLSearchParams(params); p.delete('group'); p.set('source', String(l.source_id)); setParams(p, { replace: true }) }}
                onPattern={() => { const p = new URLSearchParams(params); p.set('pattern', l.pattern); p.set('source', String(l.source_id)); p.delete('q'); setQ(''); setParams(p, { replace: true }) }} />
            </Fragment>
          )
        })}
        {more && <div className="list-more"><button type="button" className="btn" disabled={busy} onClick={older}>{busy && <Spinner />}Older lines</button></div>}
      </div>
    </div>
  )
}

function LogRow({ line: l, terms, open, fresh, onToggle, onSource, onPattern }: { line: LogLine; terms: string[]; open: boolean; fresh: boolean; onToggle: () => void; onSource: () => void; onPattern: () => void }) {
  const t = lineTime(l.at)
  const gi = GROUPS.findIndex(g => g.id === l.group) + 1
  return (
    <div className={`log-row lv-${l.level} ${open ? 'open' : ''} ${fresh ? 'fresh' : ''}`}>
      <button type="button" className="log-line" onClick={onToggle} aria-expanded={open}>
        <span className="log-t tnum" title={new Date(l.at).toString()}>{t.day && <span className="log-d">{t.day} </span>}{t.clock}</span>
        <LevelTag level={l.level} />
        <span className="log-src" title={l.source}><span className={`gdot g${gi}`} aria-hidden="true" />{l.source_label}</span>
        <span className="log-msg"><Mark text={l.msg} terms={terms} />{!open && l.attrs && <span className="log-attrs-inline">{Object.entries(l.attrs).slice(0, 4).map(([k, v]) => <span key={k}> <i>{k}=</i>{v.length > 60 ? v.slice(0, 60) + '…' : v}</span>)}</span>}</span>
      </button>
      {open && <LineDetail line={l} terms={terms} onSource={onSource} onPattern={onPattern} />}
    </div>
  )
}

function LineDetail({ line: l, terms, onSource, onPattern }: { line: LogLine; terms: string[]; onSource: () => void; onPattern: () => void }) {
  const [ctx, setCtx] = useState<LogLine[] | null>(null)
  const [busy, setBusy] = useState(false)
  const { toast } = useToast()
  const around = async () => {
    setBusy(true)
    try { setCtx((await logsApi.context(l.id, 20)).lines) } catch (e) { toast(errMsg(e), 'err') } finally { setBusy(false) }
  }
  return (
    <div className="log-detail">
      <pre className="log-full"><Mark text={l.msg} terms={terms} /></pre>
      {l.attrs && (
        <dl className="log-kv">
          {Object.entries(l.attrs).map(([k, v]) => <Fragment key={k}><dt>{k}</dt><dd><Mark text={v} terms={terms} /></dd></Fragment>)}
        </dl>
      )}
      <div className="log-actions">
        <button type="button" className="btn sm" onClick={around} disabled={busy}>{busy ? <Spinner /> : <Icon name="list" size={14} />}Lines around it</button>
        <button type="button" className="btn ghost sm" onClick={onSource}><Icon name="server" size={14} />Only {l.source_label}</button>
        <button type="button" className="btn ghost sm" onClick={onPattern}><Icon name="repeat" size={14} />Same message</button>
        <button type="button" className="btn ghost sm" onClick={() => { navigator.clipboard?.writeText(`${new Date(l.at).toISOString()} ${l.level.toUpperCase()} ${l.source} ${l.msg}${l.attrs ? ' ' + JSON.stringify(l.attrs) : ''}`); toast('Copied') }}><Icon name="copy" size={14} />Copy</button>
        <span className="muted tiny">{l.source}{l.stream ? ` · ${l.stream}` : ''} · {new Date(l.at).toLocaleString()}</span>
      </div>
      {ctx && (
        <div className="log-context" ref={el => { const me = el?.querySelector<HTMLElement>('.me'); if (el && me) el.scrollTop = me.offsetTop - el.clientHeight / 2 }}>
          {ctx.map(c => (
            <div key={c.id} className={`ctx-row lv-${c.level} ${c.id === l.id ? 'me' : ''}`}>
              <span className="log-t tnum">{lineTime(c.at).clock}</span><LevelTag level={c.level} /><span className="log-msg">{c.msg}</span>
            </div>
          ))}
        </div>
      )}
    </div>
  )
}

// ---------------- Patterns ----------------

function PatternsTab({ go }: { go: (t: Tab, q?: URLSearchParams) => void }) {
  const [range, setRange] = useRange()
  const [params, setParams] = useSearchParams()
  const sort = params.get('sort') || 'top'
  const [list, setList] = useState<Pattern[] | null>(null)
  const [all, setAll] = useState(0)
  useEffect(() => {
    setList(null)
    logsApi.patterns(`range=${range}&sort=${sort}&limit=60`).then(r => { setList(r.patterns); setAll(r.lines) }).catch(() => setList([]))
  }, [range, sort])
  const covered = list ? list.reduce((s, p) => s + p.n, 0) : 0
  return (
    <div className="logs-grid one">
      <div className="logs-bar">
        <RangePicker value={range} onChange={setRange} />
        <Segmented size="sm" label="Which" options={[{ value: 'top', label: 'Most lines' }, { value: 'errors', label: 'Problems' }, { value: 'new', label: 'New' }]} value={sort}
          onChange={v => { const p = new URLSearchParams(params); p.set('sort', v); setParams(p, { replace: true }) }} />
        {list && all > 0 && sort === 'top' && <span className="muted small">These {list.length} messages are {Math.round(covered / all * 100)}% of all {fmtCount(all)} lines — set a noisy source to count only in Sources.</span>}
      </div>
      <Card flush>
        <PatternList list={list} go={go} empty={sort === 'new' ? 'Nothing new in this span.' : 'No messages yet.'} />
      </Card>
    </div>
  )
}

// ---------------- Web traffic ----------------

function webSeries(w: Web): StackSeries[] {
  return [
    { key: '5xx', label: 'Failed (5xx)', cls: 'web-5', values: w.classes['5xx'] },
    { key: '4xx', label: 'Refused (4xx)', cls: 'web-4', values: w.classes['4xx'] },
    { key: '3xx', label: 'Redirected (3xx)', cls: 'web-3', values: w.classes['3xx'] },
    { key: '2xx', label: 'OK (2xx)', cls: 'web-2', values: w.classes['2xx'] },
  ].filter(s => s.values.some(v => v))
}

function WebTab({ go }: { go: (t: Tab, q?: URLSearchParams) => void }) {
  const [range, setRange] = useRange()
  const [web, setWeb] = useState<Web | null>(null)
  const [sort, setSort] = useState<'n' | 'server_errors' | 'avg_ms'>('n')
  useEffect(() => { setWeb(null); logsApi.web(`range=${range}`).then(setWeb).catch(() => setWeb(null)) }, [range])
  if (!web) return <Loading />
  if (!web.total) return <Empty title="No web traffic yet" icon="globe">Errors reads Caddy’s access log (JSON) from its container. Switch on access logs in your Caddyfile (<code>log</code> in a site block) and the requests show here.</Empty>
  const sites = [...web.sites].sort((a, b) => sort === 'n' ? b.n - a.n : sort === 'avg_ms' ? b.avg_ms - a.avg_ms : (b.server_errors / Math.max(1, b.n)) - (a.server_errors / Math.max(1, a.n)) || b.server_errors - a.server_errors)
  return (
    <div className="logs-grid">
      <div className="logs-bar"><RangePicker value={range} onChange={setRange} /></div>
      <div className="kpis">
        <Kpi icon="globe" label="Requests" value={fmtCount(web.total)} sub={`${web.sites.length} sites`} />
        <Kpi icon="alert" tone={web.errors ? 'bad' : ''} label="Failed (5xx)" value={fmtCount(web.errors)} sub={`${((web.errors / web.total) * 100).toFixed(2)}% of requests`} onClick={() => go('explore', exploreQuery({ source: 'caddy', level: 'error', r: range }))} />
        <Kpi icon="lock" label="Refused (4xx)" value={fmtCount(web.client)} sub={`${((web.client / web.total) * 100).toFixed(1)}%`} />
        <Kpi icon="timer" label="Answer time" value={fmtMs(web.avg_ms)} sub="on average" />
        <Kpi icon="hourglass" tone={web.slow ? 'warn' : ''} label="Slow" value={fmtCount(web.slow)} sub="over a second" />
      </div>
      <Card title="Requests over time" sub="By how they were answered" className="span-2">
        <StackChart label="Requests by answer over time" series={webSeries(web)} from={web.from} bucket={web.bucket} unit="requests" height={170} />
      </Card>
      <Card title="Answer time" sub="The average time to answer, per bar" className="span-2">
        <LineChart label="Average answer time over time" values={web.ms} from={web.from} bucket={web.bucket} fmt={fmtMs} />
      </Card>
      <Card title="Sites" flush className="span-2" actions={<Segmented size="sm" label="Sort" options={[{ value: 'n', label: 'Busiest' }, { value: 'server_errors', label: 'Failing' }, { value: 'avg_ms', label: 'Slowest' }]} value={sort} onChange={v => setSort(v as typeof sort)} />}>
        <div className="table-wrap">
          <table className="data-table sites">
            <thead><tr><th>Site</th><th className="wide-only">Over time</th><th className="num">Requests</th><th className="num">Failed</th><th className="num wide-only">Refused</th><th className="num">Avg</th><th className="num wide-only">Slow</th></tr></thead>
            <tbody>
              {sites.map(s => (
                <tr key={s.host} className="click" onClick={() => go('explore', exploreQuery({ q: `"${s.host}"`, source: 'caddy', r: range }))}>
                  <td className="ellipsis site-host">{s.host}</td>
                  <td className="wide-only"><MiniBars values={s.spark} bad={s.error_spark} label={`${s.host}: requests over time`} width={120} height={20} /></td>
                  <td className="num tnum">{fmtCount(s.n)}</td>
                  <td className={`num tnum ${s.server_errors ? 'bad' : 'muted'}`}>{s.server_errors ? <>{fmtCount(s.server_errors)} <span className="muted tiny">{((s.server_errors / s.n) * 100).toFixed(1)}%</span></> : '—'}</td>
                  <td className="num tnum wide-only muted">{s.client_errors ? fmtCount(s.client_errors) : '—'}</td>
                  <td className={`num tnum ${s.avg_ms >= 1000 ? 'warn-text' : ''}`}>{fmtMs(s.avg_ms)}</td>
                  <td className="num tnum wide-only muted">{s.slow || '—'}</td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      </Card>
    </div>
  )
}

// ---------------- Sources ----------------

function SourcesTab({ go }: { go: (t: Tab, q?: URLSearchParams) => void }) {
  const [range, setRange] = useRange()
  const [list, setList] = useState<SourceCount[] | null>(null)
  const [total, setTotal] = useState(0)
  const [q, setQ] = useState('')
  const [sort, setSort] = useState<'lines' | 'errors' | 'name'>('lines')
  const { toast } = useToast()
  useEffect(() => { logsApi.sources(`range=${range}`).then(r => { setList(r.sources); setTotal(r.total) }).catch(e => toast(errMsg(e), 'err')) }, [range, toast])
  const setKeep = async (s: SourceCount, keep: Keep) => {
    try {
      await logsApi.setKeep(s.id, keep)
      setList(l => l && l.map(x => x.id === s.id ? { ...x, keep } : x))
      toast(`${s.label}: ${KEEPS.find(k => k.value === keep)!.label.toLowerCase()}`)
    } catch (e) { toast(errMsg(e), 'err') }
  }
  if (!list) return <Loading />
  const ql = q.trim().toLowerCase()
  const shown = list.filter(s => !ql || s.label.toLowerCase().includes(ql) || s.name.toLowerCase().includes(ql))
    .sort((a, b) => sort === 'name' ? a.label.localeCompare(b.label) : sort === 'errors' ? b.errors - a.errors || b.lines - a.lines : b.lines - a.lines)
  const max = Math.max(1, ...list.map(s => s.lines))
  return (
    <div className="logs-grid one">
      <div className="logs-bar">
        <RangePicker value={range} onChange={setRange} />
        <div className="input-icon"><Icon name="search" size={14} /><input type="search" value={q} onChange={e => setQ(e.target.value)} placeholder="Find a source" aria-label="Find a source" /></div>
        <Segmented size="sm" label="Sort" options={[{ value: 'lines', label: 'Busiest' }, { value: 'errors', label: 'Most errors' }, { value: 'name', label: 'Name' }]} value={sort} onChange={v => setSort(v as typeof sort)} />
      </div>
      <ConnectDevice hasDevices={list.some(s => s.group === 'device')} />
      {GROUPS.map((g, gi) => {
        const rows = shown.filter(s => s.group === g.id)
        if (!rows.length) return null
        const gl = rows.reduce((a, s) => a + s.lines, 0)
        return (
          <Card key={g.id} flush title={<span className="src-group-h"><span className={`gdot g${gi + 1}`} />{g.label}</span>} sub={`${rows.length} · ${fmtCount(gl)} lines${total ? ` · ${Math.round(gl / total * 100)}%` : ''} — ${g.sub}`}>
            <ul className="src-list">
              {rows.map(s => (
                <li key={s.id} className={s.lines ? '' : 'quiet'}>
                  <button type="button" className="src-main" onClick={() => go('explore', exploreQuery({ source: s.id, r: range }))}>
                    <span className="src-name"><b className="ellipsis">{s.label}</b><span className="muted tiny ellipsis">{s.label !== s.name ? s.name : ''}{s.image ? `${s.label !== s.name ? ' · ' : ''}${s.image}` : ''}</span></span>
                    <span className="src-bar wide-only" aria-hidden="true"><i style={{ width: `${(s.lines / max) * 100}%` }} /></span>
                    <MiniBars values={s.spark} label={`${s.label}: lines over time`} width={96} height={20} />
                    <span className="src-n tnum">{fmtCount(s.lines)}<span className="muted tiny"> lines</span></span>
                    <span className={`src-e tnum ${s.errors ? 'bad' : 'muted'}`}>{s.errors ? fmtCount(s.errors) : '—'}<span className="muted tiny"> err</span></span>
                    <span className="src-last muted tiny wide-only">{agoMs(s.last_at)}</span>
                  </button>
                  <Select className="src-keep" value={s.keep} onChange={v => setKeep(s, v as Keep)} ariaLabel={`What to keep of ${s.label}`} options={KEEPS.map(k => ({ value: k.value, label: k.label, hint: k.hint }))} />
                </li>
              ))}
            </ul>
          </Card>
        )
      })}
    </div>
  )
}

// ---------------- connecting a phone or computer ----------------

function CodeBlock({ code }: { code: string }) {
  const { toast } = useToast()
  return (
    <div className="code-block">
      <pre>{code}</pre>
      <button type="button" className="btn ghost icon-only sm" aria-label="Copy" title="Copy" onClick={() => { navigator.clipboard?.writeText(code); toast('Copied') }}><Icon name="copy" size={14} /></button>
    </div>
  )
}

/** How to send lines from outside the box: an iPhone (through a Mac), an app with a Sentry SDK, anything with curl. */
function ConnectDevice({ hasDevices }: { hasDevices: boolean }) {
  const [open, setOpen] = useState(!hasDevices)
  const [how, setHow] = useState<'homelab' | 'iphone' | 'app' | 'other'>('homelab')
  const base = window.location.origin
  if (!open) return <div className="logs-bar"><button type="button" className="btn" onClick={() => setOpen(true)}><Icon name="smartphone" size={15} />Connect a phone or computer</button></div>
  return (
    <Card className="connect" title={<span className="src-group-h"><Icon name="smartphone" size={16} />Connect a phone or computer</span>}
      sub="Its lines land under Phones & devices, as one source you can search, chart and follow live."
      actions={<>
        <Segmented size="sm" label="What" options={[{ value: 'homelab', label: 'Pocket Homelab' }, { value: 'iphone', label: 'iPhone log' }, { value: 'app', label: 'Another app' }, { value: 'other', label: 'Anything' }]} value={how} onChange={v => setHow(v as typeof how)} />
        {hasDevices && <button type="button" className="btn ghost icon-only sm" aria-label="Close" onClick={() => setOpen(false)}><Icon name="x" size={14} /></button>}
      </>}>
      {how === 'homelab' && <>
        <p className="connect-note">The Pocket Homelab app (build 1413 or newer) sends its own log and errors here by itself: background runs and battery use, failed requests (host, path and status only), Apple Health sync failures, and the crashes, hangs and overuse iOS reports. Addresses lose their query and any password first; nothing leaves the phone while Pocket Errors is not set up.</p>
        <ol className="connect-steps">
          <li>Through Pocket Core it is set up already. Without it: in the app, Settings → Services → Pocket Errors, the address <code>{base}</code> and a token from <Link to="/settings/tokens">Settings → API tokens</Link>.</li>
          <li>Use an admin’s account: only admins may send lines to Logs (errors go in for anyone).</li>
          <li>Lines show under Phones & devices as <b>Pocket Homelab (iPhone)</b>, errors as issues of the project of that name — and an issue shows what the phone logged two minutes either side. The phone sends when the app opens, closes or refreshes in the background; an offline day arrives later.</li>
        </ol>
      </>}
      {how === 'iphone' && <>
        <p className="connect-note">An iPhone does not hand its system log to anyone over the network by itself: a Mac it trusts reads it (over the cable, or over Wi-Fi once paired) and sends it here.</p>
        <ol className="connect-steps">
          <li>Make an API token: <Link to="/settings/tokens">Settings → API tokens</Link> (as an admin). Copy it.</li>
          <li>On the Mac, get the reader and the sending script:
            <CodeBlock code={`brew install libimobiledevice
curl -fsSL ${base}/pocket-logs.sh -o ~/pocket-logs.sh && chmod +x ~/pocket-logs.sh`} /></li>
          <li>Plug the iPhone in once and tap <b>Trust</b> (for Wi-Fi: Finder → the iPhone → “Show this iPhone when on Wi-Fi”). Then start it:
            <CodeBlock code={`export POCKET_ERRORS_URL=${base}
export POCKET_ERRORS_TOKEN=per_…   # the token from step 1
idevicesyslog | ~/pocket-logs.sh iphone "iPhone"`} />
            <span className="connect-note">Over Wi-Fi: <code>idevicesyslog -n</code>. Only one app’s lines: <code>idevicesyslog -p SpringBoard</code>. Several phones: give each its own name (<code>iphone-ana "Ana’s iPhone"</code>).</span></li>
          <li>Within a few seconds <b>iPhone</b> shows under Phones & devices. Every line carries the process it came from — search <code>SpringBoard</code>, or set it to keep only warnings and worse (an iPhone talks a lot).</li>
        </ol>
      </>}
      {how === 'app' && <>
        <p className="connect-note">An iOS (or any) app with a Sentry SDK sends its crashes and errors to a project here, and — with logs switched on in the SDK — its log lines too, as a source named after the project.</p>
        <ol className="connect-steps">
          <li>Make a project for the app (<Link to="/projects?new=1">New project</Link>, platform Other) and copy its DSN.</li>
          <li>In the app (Sentry Cocoa 8.55 or newer; in the newest SDKs it is <code>options.enableLogs</code>):
            <CodeBlock code={`SentrySDK.start { options in
    options.dsn = "<the project's DSN>"
    options.experimental.enableLogs = true
}

SentrySDK.logger.info("Opened the chat", attributes: ["screen": "chat"])`} /></li>
          <li>Errors become issues of that project; the lines land under Phones & devices with the project’s name.</li>
        </ol>
      </>}
      {how === 'other' && <>
        <p className="connect-note">Any computer or script with curl: a Mac’s or a laptop’s log, a job’s output, a Raspberry Pi. Text (one line each), NDJSON, or JSON.</p>
        <ol className="connect-steps">
          <li>Make an API token: <Link to="/settings/tokens">Settings → API tokens</Link>.</li>
          <li>Pipe anything into the script (<code>curl -fsSL {base}/pocket-logs.sh -o pocket-logs.sh</code>):
            <CodeBlock code={`export POCKET_ERRORS_URL=${base} POCKET_ERRORS_TOKEN=per_…
log stream --style syslog | pocket-logs.sh macbook "MacBook"
some-job 2>&1 | pocket-logs.sh nightly-job "Nightly job"`} /></li>
          <li>Or send them yourself:
            <CodeBlock code={`curl -X POST "${base}/api/v1/logs/push" \
  -H "Authorization: Bearer per_…" -H "Content-Type: application/json" \
  -d '{"source":"my-app","name":"My app","lines":[{"level":"error","message":"sync failed","fields":{"code":500}}]}'`} />
            <span className="connect-note">Up to 1000 lines (1 MB) at a time; <code>at</code> is an RFC 3339 time (else now). Sent twice, a line is kept once.</span></li>
        </ol>
      </>}
    </Card>
  )
}
