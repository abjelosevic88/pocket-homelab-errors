// Charts for the Logs page: stacked bars over time (drag across them to
// zoom), a heat map of sources by time, a line for answer times, small bars
// for rows and a meter. Plain SVG drawn at the element's real width; every
// mark has a hover read-out, every chart with more than one series has a
// legend with its numbers, and each can show its numbers as a table.
import { useCallback, useMemo, useRef, useState } from 'react'
import { barLabel, fmtCount } from '../lib/logs'

export function useWidth(fallback: number) {
  const [w, setW] = useState(fallback)
  const ro = useRef<ResizeObserver | null>(null)
  const ref = useCallback((el: Element | null) => {
    ro.current?.disconnect()
    ro.current = null
    if (!el) return
    const measure = () => { const cw = Math.round(el.getBoundingClientRect().width); if (cw > 0) setW(cw) }
    measure()
    ro.current = new ResizeObserver(measure)
    ro.current.observe(el)
  }, [])
  return [ref, w] as const
}

/** 1, 2, 2.5, 5 × 10ⁿ at or above v. */
function niceMax(v: number): number {
  if (v <= 4) return Math.max(1, Math.ceil(v))
  const p = Math.pow(10, Math.floor(Math.log10(v)))
  for (const m of [1, 2, 2.5, 5, 10]) if (m * p >= v) return m * p
  return 10 * p
}

/** A bar whose top corners are round and whose foot sits square on the baseline. */
function topRounded(x: number, y: number, w: number, h: number, r: number): string {
  r = Math.min(r, w / 2, h)
  return `M${x},${y + h}V${y + r}Q${x},${y} ${x + r},${y}H${x + w - r}Q${x + w},${y} ${x + w},${y + r}V${y + h}Z`
}

export interface StackSeries { key: string; label: string; cls: string; values: number[] }

const GUTTER = 36

/** Stacked bars over time. Point at a bar for its numbers; drag across bars to pick a span (onSelect). */
export function StackChart({ series, from, bucket, height = 160, unit = 'lines', onSelect, onPick, legend = true, label }: {
  series: StackSeries[]; from: number; bucket: number; height?: number; unit?: string
  onSelect?: (from: number, to: number) => void; onPick?: (at: number) => void; legend?: boolean; label: string
}) {
  const [ref, W] = useWidth(640)
  const [hover, setHover] = useState<number | null>(null)
  const [drag, setDrag] = useState<{ a: number; b: number } | null>(null)
  const [table, setTable] = useState(false)
  const svgRef = useRef<SVGSVGElement>(null)
  const n = series[0]?.values.length || 0
  const totals = useMemo(() => Array.from({ length: n }, (_, i) => series.reduce((s, x) => s + (x.values[i] || 0), 0)), [series, n])
  const top = niceMax(Math.max(0, ...totals))
  const plotW = Math.max(40, W - GUTTER)
  const slot = plotW / Math.max(1, n)
  const gap = slot > 6 ? 2 : slot > 3 ? 1 : 0
  const bw = Math.max(1, slot - gap)
  const H = height
  const y = (v: number) => H - (v / top) * (H - 6)
  const every = Math.max(1, Math.ceil(n / Math.max(2, Math.floor(plotW / 78))))
  const idxAt = (clientX: number) => {
    const r = svgRef.current?.getBoundingClientRect()
    if (!r) return null
    const i = Math.floor((clientX - r.left - GUTTER) / slot)
    return i < 0 || i >= n ? null : i
  }
  const sum = (k: string) => series.find(s => s.key === k)?.values.reduce((a, b) => a + b, 0) || 0
  const lo = drag ? Math.min(drag.a, drag.b) : -1
  const hi = drag ? Math.max(drag.a, drag.b) : -1
  const tipI = drag ? null : hover
  return (
    <div className="lc" ref={ref}>
      {legend && series.length > 1 && (
        <div className="lc-legend" role="list" aria-label="Legend">
          {series.map(s => <span key={s.key} role="listitem" className="lc-key"><i className={`sw ${s.cls}`} aria-hidden="true" />{s.label}<b className="tnum">{fmtCount(sum(s.key))}</b></span>)}
          <button type="button" className="lc-table-btn" onClick={() => setTable(t => !t)} aria-pressed={table}>{table ? 'Chart' : 'Table'}</button>
        </div>
      )}
      {table ? <NumbersTable series={series} from={from} bucket={bucket} /> : (
        <div className="lc-plot">
          <svg ref={svgRef} width={W} height={H + 20} viewBox={`0 0 ${W} ${H + 20}`} role="img" aria-label={label}
            className={onSelect ? 'brushable' : ''}
            onPointerMove={e => { const i = idxAt(e.clientX); setHover(i); if (drag && i != null) setDrag(d => d && { ...d, b: i }) }}
            onPointerLeave={() => { setHover(null) }}
            onPointerDown={e => {
              if (!onSelect || e.pointerType === 'touch') return
              const i = idxAt(e.clientX); if (i == null) return
              ;(e.target as Element).setPointerCapture?.(e.pointerId)
              setDrag({ a: i, b: i })
            }}
            onPointerUp={e => {
              const i = idxAt(e.clientX)
              if (drag) {
                const a = Math.min(drag.a, drag.b), b = Math.max(drag.a, drag.b)
                setDrag(null)
                if (b > a) { onSelect?.(from + a * bucket, from + (b + 1) * bucket); return }
              }
              if (i != null && onPick) onPick(from + i * bucket)
            }}>
            {/* recessive grid: the top, the half and the baseline */}
            {[top, top / 2].map((v, k) => (
              <g key={k}>
                <line x1={GUTTER} x2={W} y1={y(v)} y2={y(v)} className="lc-grid" />
                <text x={GUTTER - 6} y={y(v) + 4} className="lc-ytick" textAnchor="end">{fmtCount(v)}</text>
              </g>
            ))}
            <line x1={GUTTER} x2={W} y1={H} y2={H} className="lc-base" />
            {drag && <rect x={GUTTER + lo * slot} y={0} width={(hi - lo + 1) * slot} height={H} className="lc-brush" />}
            {tipI != null && <rect x={GUTTER + tipI * slot} y={0} width={slot} height={H} className="lc-col" />}
            {totals.map((t, i) => {
              if (!t) return null
              let acc = 0
              const x = GUTTER + i * slot + gap / 2
              const segs = series.filter(s => s.values[i] > 0)
              return (
                <g key={i}>
                  {segs.map((s, k) => {
                    const v = s.values[i]
                    const y0 = y(acc), y1 = y(acc + v)
                    acc += v
                    const sep = k > 0 ? 1 : 0 // a hairline of surface between stacked parts
                    const h = Math.max(1, y0 - y1 - sep)
                    const last = k === segs.length - 1
                    return last
                      ? <path key={s.key} d={topRounded(x, y0 - sep - h, bw, h, Math.min(4, bw / 2))} className={`lc-bar ${s.cls}`} />
                      : <rect key={s.key} x={x} y={y0 - sep - h} width={bw} height={h} className={`lc-bar ${s.cls}`} />
                  })}
                </g>
              )
            })}
            {Array.from({ length: n }, (_, i) => i % every === 0 && (
              <text key={i} x={GUTTER + i * slot} y={H + 14} className="lc-xtick">{barLabel(from + i * bucket, bucket)}</text>
            ))}
          </svg>
          {tipI != null && (
            <div className="lc-tip" style={{ left: Math.min(Math.max(GUTTER + tipI * slot + slot / 2, 90), W - 90) }}>
              <div className="lc-tip-h">{barLabel(from + tipI * bucket, bucket, true)}</div>
              <div className="lc-tip-total"><b className="tnum">{totals[tipI].toLocaleString()}</b> {unit}</div>
              {series.length > 1 && series.filter(s => s.values[tipI]).map(s => (
                <div key={s.key} className="lc-tip-row"><i className={`sw ${s.cls}`} />{s.label}<b className="tnum">{s.values[tipI].toLocaleString()}</b></div>
              ))}
              {onSelect && <div className="lc-tip-hint">Drag across bars to zoom in</div>}
            </div>
          )}
        </div>
      )}
    </div>
  )
}

function NumbersTable({ series, from, bucket }: { series: StackSeries[]; from: number; bucket: number }) {
  const n = series[0]?.values.length || 0
  return (
    <div className="lc-table-wrap">
      <table className="lc-table">
        <thead><tr><th>From</th>{series.map(s => <th key={s.key} className="num">{s.label}</th>)}</tr></thead>
        <tbody>
          {Array.from({ length: n }, (_, i) => n - 1 - i).filter(i => series.some(s => s.values[i])).map(i => (
            <tr key={i}><td>{barLabel(from + i * bucket, bucket, true)}</td>{series.map(s => <td key={s.key} className="num tnum">{(s.values[i] || 0).toLocaleString()}</td>)}</tr>
          ))}
        </tbody>
      </table>
    </div>
  )
}

/** Sources (rows) by time (columns); a cell's shade is its count, one hue. */
export function Heatmap({ rows, values, from, bucket, mode, onPick }: {
  rows: { id: number; label: string }[]; values: number[][]; from: number; bucket: number; mode: 'lines' | 'errors'
  onPick?: (row: number, from: number, to: number) => void
}) {
  const [ref, W] = useWidth(640)
  const [hover, setHover] = useState<{ r: number; c: number } | null>(null)
  const labelW = Math.min(150, Math.max(90, W * .22))
  const n = values[0]?.length || 0
  // merge columns until a cell is at least 7 px wide
  let merge = 1
  while (n / merge > 1 && (W - labelW) / Math.ceil(n / merge) < 7) merge++
  const cols = Math.ceil(n / merge)
  const cells = values.map(row => Array.from({ length: cols }, (_, c) => row.slice(c * merge, c * merge + merge).reduce((a, b) => a + b, 0)))
  const max = Math.max(1, ...cells.flat())
  const cw = (W - labelW) / Math.max(1, cols)
  const ch = 20
  const H = rows.length * (ch + 2)
  const shade = (v: number) => v ? Math.max(.12, Math.sqrt(v / max)) : 0
  const b = bucket * merge
  return (
    <div className="lc heat" ref={ref}>
      <div className="lc-plot">
        <svg width={W} height={H + 18} viewBox={`0 0 ${W} ${H + 18}`} role="img" aria-label={`${mode === 'errors' ? 'Errors' : 'Lines'} per source over time`} onPointerLeave={() => setHover(null)}>
          {rows.map((r, ri) => (
            <g key={r.id}>
              <text x={labelW - 8} y={ri * (ch + 2) + ch / 2 + 4} textAnchor="end" className={`heat-label ${hover?.r === ri ? 'on' : ''}`}>{r.label.length > 20 ? r.label.slice(0, 19) + '…' : r.label}</text>
              {cells[ri].map((v, c) => (
                <rect key={c} x={labelW + c * cw + 1} y={ri * (ch + 2)} width={Math.max(1, cw - 2)} height={ch} rx={Math.min(3, cw / 3)}
                  className={`heat-cell ${mode} ${v ? '' : 'zero'} ${hover?.r === ri && hover?.c === c ? 'hl' : ''}`}
                  style={v ? { fillOpacity: shade(v) } : undefined}
                  onPointerEnter={() => setHover({ r: ri, c })}
                  onClick={() => onPick?.(r.id, from + c * b, from + (c + 1) * b)} />
              ))}
            </g>
          ))}
          {Array.from({ length: cols }, (_, c) => c % Math.max(1, Math.ceil(cols / Math.max(2, Math.floor((W - labelW) / 78)))) === 0 && (
            <text key={c} x={labelW + c * cw} y={H + 12} className="lc-xtick">{barLabel(from + c * b, b)}</text>
          ))}
        </svg>
        {hover && (
          <div className="lc-tip" style={{ left: Math.min(Math.max(labelW + hover.c * cw + cw / 2, 90), W - 90), top: hover.r * (ch + 2) + ch + 6 }}>
            <div className="lc-tip-h">{rows[hover.r].label}</div>
            <div className="muted tiny">{barLabel(from + hover.c * b, b, true)}</div>
            <div className="lc-tip-total"><b className="tnum">{cells[hover.r][hover.c].toLocaleString()}</b> {mode === 'errors' ? (cells[hover.r][hover.c] === 1 ? 'error' : 'errors') : 'lines'}</div>
            {onPick && <div className="lc-tip-hint">Click to read these lines</div>}
          </div>
        )}
      </div>
      <div className="heat-scale" aria-hidden="true"><span>0</span><i className={`heat-ramp ${mode}`} /><span>{fmtCount(max)}</span></div>
    </div>
  )
}

/** One series over time as a line with a soft area (answer times). */
export function LineChart({ values, from, bucket, height = 110, fmt, label }: { values: number[]; from: number; bucket: number; height?: number; fmt: (v: number) => string; label: string }) {
  const [ref, W] = useWidth(640)
  const [hover, setHover] = useState<number | null>(null)
  const svgRef = useRef<SVGSVGElement>(null)
  const n = values.length
  const top = niceMax(Math.max(0, ...values))
  const plotW = Math.max(40, W - GUTTER)
  const step = plotW / Math.max(1, n - 1)
  const H = height
  const px = (i: number) => GUTTER + i * step
  const py = (v: number) => H - (v / top) * (H - 8)
  // gaps where nothing was asked stay gaps
  const parts: string[] = []
  let cur = ''
  values.forEach((v, i) => {
    if (!v) { if (cur) parts.push(cur); cur = ''; return }
    cur += `${cur ? 'L' : 'M'}${px(i).toFixed(1)},${py(v).toFixed(1)}`
  })
  if (cur) parts.push(cur)
  const every = Math.max(1, Math.ceil(n / Math.max(2, Math.floor(plotW / 78))))
  return (
    <div className="lc" ref={ref}>
      <div className="lc-plot">
        <svg ref={svgRef} width={W} height={H + 20} viewBox={`0 0 ${W} ${H + 20}`} role="img" aria-label={label}
          onPointerMove={e => { const r = svgRef.current!.getBoundingClientRect(); const i = Math.round((e.clientX - r.left - GUTTER) / step); setHover(i >= 0 && i < n ? i : null) }}
          onPointerLeave={() => setHover(null)}>
          {[top, top / 2].map((v, k) => (
            <g key={k}>
              <line x1={GUTTER} x2={W} y1={py(v)} y2={py(v)} className="lc-grid" />
              <text x={GUTTER - 6} y={py(v) + 4} className="lc-ytick" textAnchor="end">{fmt(v)}</text>
            </g>
          ))}
          <line x1={GUTTER} x2={W} y1={H} y2={H} className="lc-base" />
          {parts.map((d, k) => <path key={k} d={d} className="lc-line" />)}
          {hover != null && <line x1={px(hover)} x2={px(hover)} y1={0} y2={H} className="lc-cross" />}
          {hover != null && values[hover] > 0 && <circle cx={px(hover)} cy={py(values[hover])} r={4.5} className="lc-dot" />}
          {Array.from({ length: n }, (_, i) => i % every === 0 && <text key={i} x={px(i)} y={H + 14} className="lc-xtick">{barLabel(from + i * bucket, bucket)}</text>)}
        </svg>
        {hover != null && (
          <div className="lc-tip" style={{ left: Math.min(Math.max(px(hover), 90), W - 90) }}>
            <div className="lc-tip-h">{barLabel(from + hover * bucket, bucket, true)}</div>
            <div className="lc-tip-total">{values[hover] ? <><b className="tnum">{fmt(values[hover])}</b> on average</> : <span className="muted">No requests</span>}</div>
          </div>
        )}
      </div>
    </div>
  )
}

/** Small bars for a row (no axes); the bad part of each bar on its foot. */
export function MiniBars({ values, bad, width = 120, height = 24, label }: { values: number[]; bad?: number[]; width?: number; height?: number; label: string }) {
  const n = values.length || 1
  const max = Math.max(1, ...values)
  const slot = width / n
  const gap = slot > 4 ? 1 : 0
  const w = Math.max(.8, slot - gap)
  return (
    <svg className="mini" width={width} height={height} viewBox={`0 0 ${width} ${height}`} role="img" aria-label={label}>
      {values.map((v, i) => {
        const h = v ? Math.max(2, (v / max) * (height - 1)) : 1
        const e = bad?.[i] ? Math.max(2, (bad[i] / max) * (height - 1)) : 0
        return (
          <g key={i}>
            <rect x={i * slot} y={height - h} width={w} height={h} rx={Math.min(1.5, w / 2)} className={v ? 'mini-on' : 'mini-off'} />
            {e > 0 && <rect x={i * slot} y={height - e} width={w} height={e} className="mini-bad" />}
          </g>
        )
      })}
    </svg>
  )
}

/** How full something is: a bar with its words. */
export function Meter({ value, max, label, text }: { value: number; max: number; label: string; text: string }) {
  const p = max > 0 ? Math.min(1, value / max) : 0
  return (
    <div className="meter" role="meter" aria-valuemin={0} aria-valuemax={max} aria-valuenow={value} aria-label={label}>
      <div className="meter-bar"><i style={{ width: `${Math.max(p * 100, value ? 1.5 : 0)}%` }} className={p > .9 ? 'hot' : p > .7 ? 'warm' : ''} /></div>
      <div className="meter-text">{text}</div>
    </div>
  )
}

/** Parts of a whole on one bar, each labelled below with its share. */
export function ShareBar({ parts, onPick }: { parts: { key: string; label: string; cls: string; value: number }[]; onPick?: (key: string) => void }) {
  const total = parts.reduce((s, p) => s + p.value, 0) || 1
  const shown = parts.filter(p => p.value > 0)
  return (
    <div className="share">
      <div className="share-bar">
        {shown.map(p => <i key={p.key} className={p.cls} style={{ flexGrow: p.value }} title={`${p.label}: ${p.value.toLocaleString()} (${Math.round(p.value / total * 100)}%)`} />)}
      </div>
      <div className="share-keys">
        {shown.map(p => (
          <button key={p.key} type="button" className="share-key" onClick={() => onPick?.(p.key)} disabled={!onPick}>
            <i className={`sw ${p.cls}`} aria-hidden="true" />{p.label}<b className="tnum">{fmtCount(p.value)}</b><span className="muted tnum">{Math.round(p.value / total * 100)}%</span>
          </button>
        ))}
      </div>
    </div>
  )
}
