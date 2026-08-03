import { useState } from 'react'
import { type Decision } from '../types'
import { useTheme, fontSizes } from '../theme'

interface DecisionsProps {
  decisions: Decision[]
  makeDecision: (reqID: string, decision: string) => void
}

export default function Decisions({ decisions, makeDecision }: DecisionsProps) {
  const { colors } = useTheme()
  const [inputs, setInputs] = useState<Record<string, string>>({})

  if (decisions.length === 0) {
    return <p style={{ color: colors.textDim, fontSize: fontSizes.md }}>No decisions.</p>
  }

  return (
    <>
      {decisions.map((d) => {
        const isPending = d.status === 'pending'
        return (
          <div
            key={d.request_id}
            style={{
              border: '1px solid ' + (isPending ? colors.border : (d.decision ? colors.decisionResolved : colors.border)),
              borderRadius: 8,
              padding: 16,
              marginBottom: 12,
              opacity: isPending ? 1 : 0.75,
            }}
          >
            <div style={{ fontSize: fontSizes.sm, color: colors.textDim, marginBottom: 8, display: 'flex', gap: 8 }}>
              <span>{d.worker_id}</span>
              <span style={{ color: colors.textDimmed, fontSize: fontSizes.xs }}>
                {new Date(d.created_at * 1000).toLocaleString()}
              </span>
              {!isPending && (
                <span style={{ color: colors.toolCompleted, fontSize: fontSizes.xs }}>resolved</span>
              )}
            </div>
            <div style={{ fontWeight: 'bold', color: colors.text, marginBottom: 4, fontSize: fontSizes.lg }}>{d.summary}</div>
            <div style={{ fontSize: fontSizes.md, color: colors.textDimmed, marginTop: 4, marginBottom: 12 }}>{d.context}</div>

            {d.decision && (
              <div style={{ marginBottom: 12, padding: '8px 10px', background: colors.bgLight, borderRadius: 6 }}>
                <div style={{ fontSize: fontSizes.sm, color: colors.toolCompleted, marginBottom: 2 }}>{d.decision}</div>
                {d.reasoning && <div style={{ fontSize: fontSizes.sm, color: colors.textDimmed }}>{d.reasoning}</div>}
              </div>
            )}

            {isPending && d.options && d.options.length > 0 && (
              <div style={{ display: 'flex', gap: 8, flexWrap: 'wrap' }}>
                {d.options.map((o) => (
                  <button
                    key={o.id}
                    onClick={() => makeDecision(d.request_id, o.id)}
                    className="decision-opt"
                    style={{
                      background: colors.bgLighter,
                      color: colors.text,
                      border: '1px solid ' + colors.border,
                      padding: '6px 14px',
                      borderRadius: 4,
                      cursor: 'pointer',
                      fontSize: fontSizes.md,
                    }}
                  >
                    {o.label}
                  </button>
                ))}
              </div>
            )}
            {isPending && (!d.options || d.options.length === 0) && (
              <div style={{ display: 'flex', gap: 8 }}>
                <input
                  value={inputs[d.request_id] || ''}
                  onChange={(e) => setInputs(prev => ({ ...prev, [d.request_id]: e.target.value }))}
                  onKeyDown={(e) => {
                    if (e.key === 'Enter') {
                      const val = inputs[d.request_id]
                      if (val && val.trim()) {
                        makeDecision(d.request_id, val.trim())
                      }
                    }
                  }}
                  placeholder="Type your response..."
                  style={{
                    flex: 1,
                    background: colors.bgLighter,
                    color: colors.text,
                    border: '1px solid ' + colors.border,
                    padding: '6px 10px',
                    borderRadius: 4,
                    fontSize: fontSizes.md,
                    fontFamily: 'monospace',
                    outline: 'none',
                  }}
                />
                <button
                  onClick={() => {
                    const val = inputs[d.request_id]
                    if (val && val.trim()) {
                      makeDecision(d.request_id, val.trim())
                    }
                  }}
                  style={{
                    background: colors.accent,
                    color: '#fff',
                    border: 'none',
                    padding: '6px 14px',
                    borderRadius: 4,
                    cursor: 'pointer',
                    fontSize: fontSizes.md,
                  }}
                >
                  Send
                </button>
              </div>
            )}
          </div>
        )
      })}
    </>
  )
}