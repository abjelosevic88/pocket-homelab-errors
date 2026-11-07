export interface User { id: number; email: string; name: string; role: 'admin' | 'member'; created_at: string }
export interface APIToken { id: number; user_id: number; name: string; prefix: string; last_used_at: string | null; created_at: string }

export type ConnMode = 'core' | 'direct' | 'off'
export type ConnVia = '' | 'core' | 'direct'
export type AppSlug = 'alerts' | 'projects' | 'tasks'

type ConnFields = { [K in AppSlug as `${K}_mode`]: ConnMode } & { [K in AppSlug as `${K}_via`]: ConnVia } & { [K in AppSlug as `${K}_detail` | `${K}_url` | `${K}_token` | `${K}_public_url` | `${K}_open_url`]: string } & { [K in AppSlug as `${K}_configured`]: boolean }

export type NotifyVia = 'auto' | 'alerts' | 'push' | 'both' | 'off'

export type Settings = ConnFields & {
  core_configured: boolean
  core_registered?: boolean
  title: string; date_format: string; time_format: string; week_start: number
  keep_days: number; scrub_ips: boolean; ai_help: boolean
  notify_via: NotifyVia; notify_url: string; notify_format: string
  watch_docker: boolean; watch_pattern: string; docker_available: boolean; is_admin: boolean
  projects_team: string; ingest_url: string; public_url: string
  ai_provider: 'auto' | 'pocket' | 'openai'; ai_pocket_model: string; openai_api_key: string; openai_model: string; openai_base_url: string
  setup_complete: boolean; base_url: string; demo: boolean
}

export type AppStateName = 'on' | 'partly' | 'off' | 'link_off' | 'missing' | 'old' | 'no_core' | 'not_set_up' | 'error'
export interface AppState {
  slug: string; name: string; reason: string; mode: ConnMode; via: ConnVia
  state: AppStateName; detail: string; fix: string; on: string[]; open_url: string
}

export type Level = 'fatal' | 'error' | 'warning' | 'info' | 'debug'
export type Status = 'open' | 'resolved' | 'ignored'
export type Platform = 'javascript' | 'go' | 'python' | 'node' | 'docker' | 'other'

export interface Project {
  id: number; name: string; platform: Platform; public_key: string; source: string; container: string; app_slug: string
  notify_new: boolean; notify_again: boolean; notify_spike: number; min_level: 'error' | 'warning'; rate_limit: number; archived: boolean
  last_event_at: string | null; created_at: string; updated_at: string
  open: number; events_24h: number; hours?: number[]
  dsn: string; dsn_local: string; store_url: string; script_url: string
}

export interface Issue {
  id: number; project_id: number; fingerprint: string; title: string; culprit: string; type: string; level: Level; status: Status; regressed: boolean
  times: number; first_seen: string; last_seen: string; first_release: string; last_release: string; environment: string
  resolved_at: string | null; ignored_until: string | null; note: string; ai_text: string; ai_at: string | null; updated_at: string
  project_name: string; app_slug: string; people: number; events_24h: number; hours?: number[]; new: boolean
}

export interface IssueLink { id: number; issue_id: number; app: string; ref: string; title: string; url: string; created_at: string }

export interface IssueView extends Issue {
  url: string; project: Project; links: IssueLink[]; tags: Record<string, Record<string, number>>
}

export interface Frame {
  function?: string; module?: string; file?: string; line?: number; col?: number; in_app: boolean
  pre_context?: string[]; context_line?: string; post_context?: string[]
}
export interface Exception { type: string; value: string; module?: string; mechanism?: string; handled?: boolean; frames?: Frame[] }
export interface Breadcrumb { at?: string; type?: string; category?: string; level?: string; message?: string; data?: Record<string, string> }
export interface EventData {
  exceptions?: Exception[]; message?: string; logger?: string; transaction?: string; breadcrumbs?: Breadcrumb[]
  request?: { method?: string; url?: string; query?: string; headers?: [string, string][]; data?: string }
  user?: Record<string, string>; tags?: [string, string][]; contexts?: Record<string, [string, string][]>; extra?: Record<string, unknown>
  server_name?: string; sdk?: string; log_lines?: string[]; fingerprint?: string[]
}
export interface ErrorEvent {
  id: number; project_id: number; issue_id: number; event_id: string; at: string; received_at: string; level: Level; message: string
  environment: string; release: string; platform: string; who: string; data: EventData
}
export interface EventPage { event: ErrorEvent | null; pos: number; total: number; newer: number; older: number }

export interface Overview {
  open: number; new: number; regressed: number; events_24h: number; projects: number; quiet_projects: number
  hours: Record<'fatal' | 'error' | 'warning' | 'info', number[]>
}

export interface WatchedContainer { name: string; image: string; project_id: number; archived: boolean; errors: number; last_error_at: string }
export interface WatchState { on: boolean; checked_at: string; error: string; containers: WatchedContainer[] }

export interface Summary { overview: Overview; latest: Issue[]; watch: WatchState }
