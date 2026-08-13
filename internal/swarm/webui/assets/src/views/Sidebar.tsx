import { useState } from 'react'
import { useTheme, fontSizes } from '../theme'
import { type WorkerInfo } from '../types'

interface SidebarProps {
  view: 'talk' | 'events'
  setView: (v: 'talk' | 'events') => void
  filterWorker: string
  setFilterWorker: (id: string) => void
  workers: WorkerInfo[]
  talkWorkers: Set<string>
  onToggleWorker: (id: string) => void
}

export default function Sidebar({ view, setView, filterWorker, setFilterWorker, workers, talkWorkers, onToggleWorker }: SidebarProps) {
  const [rotation, setRotation] = useState(0)
  const { dark, toggle, colors } = useTheme()

  const handleWorkerClick = (id: string) => {
    if (view === 'talk') {
      onToggleWorker(id)
    } else {
      setFilterWorker(filterWorker === id ? '' : id)
    }
  }

  return (
    <div
      style={{
        width: 260,
        minWidth: 260,
        flexShrink: 0,
        borderRight: '1px solid ' + colors.border,
        padding: 30,
        display: 'flex',
        flexDirection: 'column',
        gap: 8,
      }}
    >
      {/* Logo */}
      <div style={{ marginBottom: 30 }}>
        <h2 style={{ margin: 0, color: colors.accent, fontSize: fontSizes.h2, fontFamily: 'Monaco, monospace' }}>
          <span
            onClick={() => setRotation(r => r + 360)}
            style={{ cursor: 'pointer', display: 'inline-block', transform: `perspective(200px) rotateY(${rotation}deg)`, transition: 'transform 1s ease-in-out' }}
          >
            <span style={{ display: 'inline-block', transform: 'scaleX(-1)' }}>n</span>
            <span style={{ display: 'inline-block', transform: 'scaleX(-1)' }}>i</span>
            <span style={{ display: 'inline-block', transform: 'scaleX(-1)' }}>p</span>
          </span>
        </h2>
      </div>

      {/* View selector */}
      <strong style={{ marginBottom: 10, color: colors.text, fontSize: fontSizes.xl }}>View</strong>
      {(['talk', 'events'] as const).map((v) => (
        <div
          key={v}
          onClick={() => setView(v)}
          style={{ cursor: 'pointer', color: view === v ? colors.accent : colors.textDim, fontSize: fontSizes.md, lineHeight: '20px' }}
        >
          {v === 'talk' ? 'Talk' : 'Events'}{view === v ? ' \u25C9' : ''}
        </div>
      ))}

      <hr style={{ border: 'none', borderTop: '1px solid ' + colors.border, margin: '16px 0' }} />

      {/* Worker list */}
      <strong style={{ marginBottom: 10, color: colors.text, fontSize: fontSizes.xl }}>Workers</strong>
      {view === 'talk' && (
        <div style={{ fontSize: fontSizes.sm, lineHeight: '20px', color: colors.textDimmed, marginBottom: 8, fontStyle: 'italic' }}>
          Select workers to view their events. Messages are sent to the first selected reason worker.
        </div>
      )}
      {workers.filter(w => view !== 'talk' || w.type === 'reason').map((w) => {
        const isActive = view === 'talk'
          ? talkWorkers.has(w.id)
          : filterWorker === w.id
        return (
          <div
            key={w.id}
            style={{ cursor: 'pointer', color: isActive ? colors.accent : colors.textDim, fontSize: fontSizes.md }}
            onClick={() => handleWorkerClick(w.id)}
          >
            {isActive ? '\u2611 ' : '\u2610 '}
            {w.id}
            {w.type ? (
              <span
                style={{
                  color: isActive ? colors.accentDim : colors.textDimmed,
                  fontStyle: 'italic',
                }}
              >
                {': ' + w.type}
              </span>
            ) : ''}
          </div>
        )
      })}

      {/* Spacer + theme toggle at bottom */}
      <div style={{ flex: 1 }} />
      <div
        onClick={toggle}
        style={{ cursor: 'pointer', color: colors.textDim, fontSize: fontSizes.sm, marginTop: 16 }}
      >
        {dark ? 'light' : 'dark'}
      </div>
    </div>
  )
}