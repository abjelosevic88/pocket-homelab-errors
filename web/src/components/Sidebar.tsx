// Left column: a new project, the issue lists, then every project with what is open in it.
import { Link, NavLink, useLocation, useNavigate } from 'react-router-dom'
import { useApp } from '../lib/app-context'
import { useData } from '../lib/data'
import { projectIcon } from '../lib/errors'
import { Icon } from './icons'

export function Sidebar({ onCreate, onNavigate }: { onCreate: () => void; onNavigate: () => void }) {
  const { status, settings } = useApp()
  const { summary, projects } = useData()
  const navigate = useNavigate()
  const loc = useLocation()
  const title = settings?.title || status?.title || 'Pocket Errors'
  const path = loc.pathname
  const q = new URLSearchParams(loc.search)
  const onIssues = path === '/'
  const st = q.get('status') || 'open'
  const nav: { to: string; label: string; icon: string; active: boolean; count?: number }[] = [
    { to: '/', label: 'Open issues', icon: 'inbox', active: onIssues && st === 'open' && !q.get('project'), count: summary?.overview.open || undefined },
    { to: '/?status=resolved', label: 'Resolved', icon: 'check-circle', active: onIssues && st === 'resolved' && !q.get('project') },
    { to: '/?status=ignored', label: 'Ignored', icon: 'eye-off', active: onIssues && st === 'ignored' && !q.get('project') },
    { to: '/projects', label: 'Projects', icon: 'layers', active: path.startsWith('/projects') },
  ]
  const list = (projects || []).filter(p => !p.archived)
  return (
    <aside className="sidebar" aria-label="Pocket Errors">
      <div className="sb-brand">
        <img src="/favicon.svg" alt="" className="sb-logo" />
        <span className="sb-title">{title !== 'Pocket Errors' ? title : <>Pocket <b>Errors</b></>}</span>
        {status?.version && <span className="sb-version" title={`Version ${status.version}`}>{/^v?\d+\.\d+/.test(status.version) ? 'v' + status.version.replace(/^v/, '').replace(/[-+].*$/, '') : status.version}</span>}
      </div>
      <button type="button" className="btn primary create-btn" onClick={onCreate} title="New project (N)">
        <Icon name="plus" size={15} strokeWidth={2.2} />New project
      </button>

      <nav className="sb-nav" aria-label="Sections">
        {nav.map(n => (
          <NavLink key={n.to} to={n.to} onClick={onNavigate} className={() => `sb-nav-item ${n.active ? 'active' : ''}`}>
            <Icon name={n.icon} size={16} />{n.label}
            {!!n.count && <span className="sb-count">{n.count}</span>}
          </NavLink>
        ))}
      </nav>

      <section className="sb-sec">
        <h4 className="sb-h">Projects</h4>
        {list.map(p => (
          <button key={p.id} type="button" className={`sb-row sb-list ${onIssues && q.get('project') === String(p.id) ? 'active' : ''}`} onClick={() => { onNavigate(); navigate(`/?project=${p.id}`) }} title={p.container ? `Container ${p.container}` : p.name}>
            <span className="sb-place-ico" aria-hidden="true"><Icon name={projectIcon(p)} size={14} /></span>
            <span className="grow ellipsis">{p.name}</span>
            {p.open > 0 ? <span className="sb-open tnum">{p.open}</span> : <Icon name="check" size={13} className="sb-quiet" />}
          </button>
        ))}
        {!list.length && <p className="sb-empty muted tiny">No projects yet. Add one for a website or an app, or let Errors read this box’s containers (Settings → This box).</p>}
      </section>

      <div className="sb-foot">
        <Link to="/settings" className="sb-foot-link" onClick={onNavigate}><Icon name="settings" size={15} />Settings</Link>
        <button type="button" className="sb-foot-link" onClick={() => { onNavigate(); window.dispatchEvent(new CustomEvent('per:shortcuts')) }}><Icon name="keyboard" size={15} />Shortcuts</button>
      </div>
    </aside>
  )
}
