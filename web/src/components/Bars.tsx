// Small bar charts: a row's last 24 hours, an issue's day or month, the
// overview's hours by level. Plain SVG; nothing moves when the numbers change.
import { useCallback, useRef, useState } from 'react'

/** The element's real width, so a chart draws 1:1 and its text, dots and corners keep their shape. */
function useWidth(fallback: number) {
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

export function Spark({ values, width = 96, height = 22, label }: { values?: number[]; width?: number; height?: number; label?: string }) {
  const v = values && values.length ? values : new Array(24).fill(0)
  const max = Math.max(1, ...v)
  const gap = 1
  const w = (width - gap * (v.length - 1)) / v.length
  return (
    <svg className="spark" width={width} height={height} viewBox={`0 0 ${width} ${height}`} role="img" aria-label={label || `${v.reduce((a, b) => a + b, 0)} in the last 24 hours`}>
      {v.map((n, i) => {
        const h = n ? Math.max(2, (n / max) * (height - 1)) : 1
        return <rect key={i} x={i * (w + gap)} y={height - h} width={w} height={h} rx={1} className={n ? 'on' : 'off'} />
      })}
    </svg>
  )
}

export interface Series { key: string; label: string; values: number[]; className: string }

/** Stacked bars with a label under every few and a tooltip line on hover. */
export function Bars({ series, labels, height = 120, every = 6 }: { series: Series[]; labels: string[]; height?: number; every?: number }) {
  const [hover, setHover] = useState<number | null>(null)
  const n = labels.length
  const totals = labels.map((_, i) => series.reduce((s, x) => s + (x.values[i] || 0), 0))
  const max = Math.max(1, ...totals)
  const [ref, W] = useWidth(600)
  const gap = n > 40 ? 1 : 3
  const w = (W - gap * (n - 1)) / n
  const shown = hover ?? null
  return (
    <div className="bars" ref={ref}>
      <div className="bars-tip" aria-live="polite">
        {shown != null ? <><b>{labels[shown]}</b> · {totals[shown].toLocaleString()} {totals[shown] === 1 ? 'error' : 'errors'}{series.length > 1 && totals[shown] > 0 && <> ({series.filter(s => s.values[shown]).map(s => `${s.values[shown]} ${s.label.toLowerCase()}`).join(', ')})</>}</>
          : <span className="muted">Point at a bar for its count</span>}
      </div>
      <svg viewBox={`0 0 ${W} ${height}`} width={W} height={height} className="bars-svg" onMouseLeave={() => setHover(null)}>
        {labels.map((_, i) => {
          let y = height
          return (
            <g key={i} onMouseEnter={() => setHover(i)}>
              <rect x={i * (w + gap)} y={0} width={w + gap} height={height} className="bars-hit" />
              {series.map(s => {
                const v = s.values[i] || 0
                if (!v) return null
                const h = Math.max(2, (v / max) * (height - 4))
                y -= h
                return <rect key={s.key} x={i * (w + gap)} y={y} width={w} height={h} rx={1.5} className={`${s.className} ${hover === i ? 'hl' : ''}`} />
              })}
              {totals[i] === 0 && <rect x={i * (w + gap)} y={height - 1} width={w} height={1} className="bars-zero" />}
            </g>
          )
        })}
      </svg>
      <div className="bars-axis">
        {labels.map((l, i) => <span key={i} style={{ width: `${100 / n}%` }}>{i % every === 0 ? l : ''}</span>)}
      </div>
    </div>
  )
}
