import { useMemo, useRef, useEffect, useCallback, useState, type ReactNode } from 'react'
import Markdown from 'react-markdown'
import remarkGfm from 'remark-gfm'
import { useTheme, fontSizes } from '../theme'
import { makeMdComponents } from '../components/MarkdownComponents'
import ThinkingBlock from '../components/ThinkingBlock'
import ResponseBlock from '../components/ResponseBlock'
import TimerElapsedBlock from '../components/TimerElapsedBlock'
import {
  getInputText, isToolEvent, isReasonBoundary,
  toolContent, toolSummary, toolCallId,
  formatEventPayload, formatTime, truncate, findReferencedInput,
} from '../components/talk-utils'
import type { EventPayload, Decision } from '../types'

interface TalkViewProps {
  events: EventPayload[]
  talkPartner: string
  onTraceClick: (traceId: string) => void
  onLoadMore?: () => void
  deliveries: Record<string, string[]>
  decisions: Decision[]
  makeDecision: (reqID: string, decision: string) => void
  humanId?: string
}

const inputRenderers: Record<string, React.FC<{evt: EventPayload; onTraceClick: (id: string) => void}>> = {
  'timer.elapsed': TimerElapsedBlock,
}

export default function TalkView({ events, talkPartner, onTraceClick, onLoadMore, deliveries, decisions, makeDecision, humanId = 'hiw' }: TalkViewProps) {
  const { dark, colors } = useTheme()
  const scrollRef = useRef<HTMLDivElement>(null)
  const autoScrollRef = useRef(true)
  const [expandedContent, setExpandedContent] = useState<Set<string>>(new Set())
  const [thinkingExpanded, setThinkingExpanded] = useState(true)
  const [compactMode, setCompactMode] = useState(false)
  const [showViewControl, setShowViewControl] = useState(false)
  const viewControlRef = useRef<HTMLDivElement>(null)
  const prevEventCount = useRef(0)
  const hasInitialScrolled = useRef(false)

  const toggleToolContent = (callId: string) => {
    setExpandedContent(prev => {
      const next = new Set(prev)
      if (next.has(callId)) next.delete(callId)
      else next.add(callId)
      return next
    })
  }

  const relevantEvents = useMemo(() => {
    return events.filter(evt => {
      if (evt.type.startsWith('worker.') && evt.type !== 'worker.input') return false
      if (evt.type === 'worker.input') {
        if (evt.target_worker_id === talkPartner) return true
        if (evt.worker_id === talkPartner) return true
        const recipients = deliveries[evt.id]
        if (recipients && recipients.includes(talkPartner)) return true
        return false
      }
      if (evt.worker_id === talkPartner) return true
      if (evt.target_worker_id === talkPartner) return true
      const recipients = deliveries[evt.id]
      if (recipients && recipients.includes(talkPartner)) return true
      return false
    })
  }, [events, talkPartner, deliveries])

  useEffect(() => {
    const count = relevantEvents.length
    if (!hasInitialScrolled.current) {
      hasInitialScrolled.current = true
      prevEventCount.current = count
      return
    }
    if (count > prevEventCount.current && autoScrollRef.current && scrollRef.current) {
      setTimeout(() => {
        if (autoScrollRef.current && scrollRef.current) {
          scrollRef.current.scrollTo({ top: scrollRef.current.scrollHeight, behavior: 'smooth' })
        }
      }, 0)
    }
    prevEventCount.current = count
  }, [relevantEvents])

  const handleScroll = useCallback(() => {
    const el = scrollRef.current
    if (!el) return
    const atBottom = el.scrollHeight - el.scrollTop - el.clientHeight < 50
    autoScrollRef.current = atBottom
  }, [])

  if (!talkPartner) {
    return (
      <div style={{ display: 'flex', alignItems: 'center', justifyContent: 'center', height: '100%', color: colors.textDimmed }}>
        <p style={{ fontSize: fontSizes.md }}>Select a worker from the sidebar to start a conversation.</p>
      </div>
    )
  }

  const nodes: React.ReactNode[] = []

  // Track whether any tool call content is expanded (for dimming)
  const anyToolExpanded = expandedContent.size > 0

  // Load more button at the top of the scrollable area
  if (onLoadMore && events.length > 0) {
    nodes.push(
      <div key="load-more" style={{ textAlign: 'center', marginBottom: 16 }}>
        <span
          onClick={onLoadMore}
          style={{ cursor: 'pointer', fontSize: fontSizes.sm, color: colors.textDimmed, textDecoration: 'underline', textDecorationStyle: 'dotted' }}
        >
          load earlier events
        </span>
      </div>
    )
  }

  for (const [i, evt] of relevantEvents.entries()) {
    if (isReasonBoundary(evt.type)) continue

    // worker.input
    if (evt.type === 'worker.input') {
      const isFromHuman = evt.worker_id === humanId
      const isIncoming = evt.target_worker_id === talkPartner
      const alignRight = isFromHuman || isIncoming
      nodes.push(
        <div key={evt.id} style={{ marginBottom: 12, textAlign: alignRight ? 'right' : 'left' }}>
          <div
            style={{
              maxWidth: '70%',
              display: alignRight ? 'inline-block' : undefined,
              textAlign: 'left',
              background: isFromHuman ? colors.accentBg : alignRight ? (dark ? 'rgba(140, 120, 200, 0.15)' : 'rgba(140, 120, 200, 0.08)') : colors.bgChip,
              border: '1px solid ' + (isFromHuman ? colors.accentBorder : alignRight ? (dark ? 'rgba(140, 120, 200, 0.35)' : 'rgba(140, 120, 200, 0.25)') : colors.borderLight),
              borderRadius: alignRight ? '12px 12px 4px 12px' : 6,
              padding: alignRight ? '10px 14px' : '6px 10px',
              fontSize: alignRight ? fontSizes.base : fontSizes.sm,
              fontFamily: alignRight ? undefined : 'monospace',
              lineHeight: 1.5,
              color: colors.text,
            }}
          >
            <div style={{ fontSize: fontSizes.sm, color: colors.textDim, marginBottom: 4, display: 'flex', gap: 8 }}>
              <span>{evt.worker_id + ' → ' + (evt.target_worker_id || '?')}</span>
              <span style={{ color: colors.textDimmed, fontSize: fontSizes.sm }}>{formatTime(evt.timestamp)}</span>
            </div>
            <div className="md-content">
              <Markdown remarkPlugins={[remarkGfm]} components={makeMdComponents(dark, colors)}>{getInputText(evt)}</Markdown>
            </div>
            {evt.trace_id && (
              <div style={{ marginTop: 6, textAlign: alignRight ? 'right' : 'left' }}>
                <span
                  onClick={() => onTraceClick(evt.trace_id!)}
                  style={{ cursor: 'pointer', fontSize: fontSizes.sm, color: colors.textDimmed, textDecoration: 'underline', textDecorationStyle: 'dotted' }}
                  title="View all events in this trace"
                >
                  trace
                </span>
              </div>
            )}
          </div>
        </div>
      )
      continue
    }

    // timer.reminder — right-aligned bubble, like a message sent to the worker
    if (evt.type === 'timer.reminder') {
      // Try to extract reminder text from various payload locations
      let reminderText = (evt.payload?.text as string) || (evt.payload?.purpose as string) || ''
      if (!reminderText && evt.payload?.result) {
        const result = evt.payload.result
        if (typeof result === 'string') {
          try {
            const parsed = JSON.parse(result)
            reminderText = parsed.purpose || parsed.text || ''
          } catch {
            reminderText = result
          }
        } else if (typeof result === 'object') {
          reminderText = (result as any).purpose || (result as any).text || ''
        }
      }
      nodes.push(
        <div key={evt.id} style={{ marginBottom: 12, textAlign: 'right' }}>
          <div
            style={{
              maxWidth: '70%',
              display: 'inline-block',
              textAlign: 'left',
              background: dark ? 'rgba(0, 188, 212, 0.1)' : 'rgba(0, 188, 212, 0.06)',
              border: '1px solid ' + (dark ? 'rgba(0, 188, 212, 0.3)' : 'rgba(0, 188, 212, 0.2)'),
              borderRadius: '12px 12px 4px 12px',
              padding: '10px 14px',
              fontSize: fontSizes.base,
              lineHeight: 1.5,
              color: colors.text,
            }}
          >
            <div style={{ fontSize: fontSizes.sm, color: colors.textDim, marginBottom: 4, display: 'flex', gap: 8, flexWrap: 'wrap' }}>
              <span style={{ color: colors.eventType.timer }}>⏱ {evt.worker_id} → {evt.target_worker_id || talkPartner}</span>
              <span style={{ color: colors.textDimmed, fontSize: fontSizes.sm }}>{formatTime(evt.timestamp)}</span>
            </div>
            {reminderText && <div>{reminderText}</div>}
            {evt.trace_id && (
              <div style={{ marginTop: 6, textAlign: 'right' }}>
                <span
                  onClick={() => onTraceClick(evt.trace_id!)}
                  style={{ cursor: 'pointer', fontSize: fontSizes.sm, color: colors.textDimmed, textDecoration: 'underline', textDecorationStyle: 'dotted' }}
                  title="View all events in this trace"
                >
                  trace
                </span>
              </div>
            )}
          </div>
        </div>
      )
      continue
    }

    // decision.made — right-aligned bubble
    if (evt.type === 'decision.made') {
      const decision = (evt.payload?.decision as string) || ''
      const reasoning = (evt.payload?.reasoning as string) || ''
      nodes.push(
        <div key={evt.id} style={{ marginBottom: 12, textAlign: 'right' }}>
          <div
            style={{
              maxWidth: '70%',
              display: 'inline-block',
              textAlign: 'left',
              background: dark ? 'rgba(90, 138, 90, 0.1)' : 'rgba(90, 138, 90, 0.06)',
              border: '1px solid ' + (dark ? 'rgba(90, 138, 90, 0.3)' : 'rgba(90, 138, 90, 0.2)'),
              borderRadius: '12px 12px 4px 12px',
              padding: '10px 14px',
              fontSize: fontSizes.base,
              lineHeight: 1.5,
              color: colors.text,
            }}
          >
            <div style={{ fontSize: fontSizes.sm, color: colors.textDim, marginBottom: 4, display: 'flex', gap: 8, flexWrap: 'wrap' }}>
              <span style={{ color: colors.toolCompleted }}>✓ Decision: {evt.worker_id} → {evt.target_worker_id || talkPartner}</span>
              <span style={{ color: colors.textDimmed, fontSize: fontSizes.sm }}>{formatTime(evt.timestamp)}</span>
            </div>
            <div style={{ fontWeight: 'bold', marginBottom: reasoning ? 4 : 0 }}>{decision}</div>
            {reasoning && <div style={{ color: colors.textDimmed, fontSize: fontSizes.sm }}>{reasoning}</div>}
            {evt.trace_id && (
              <div style={{ marginTop: 6, textAlign: 'right' }}>
                <span
                  onClick={() => onTraceClick(evt.trace_id!)}
                  style={{ cursor: 'pointer', fontSize: fontSizes.sm, color: colors.textDimmed, textDecoration: 'underline', textDecorationStyle: 'dotted' }}
                  title="View all events in this trace"
                >
                  trace
                </span>
              </div>
            )}
          </div>
        </div>
      )
      continue
    }

    // Input renderers (right-side events)
    const InputRenderer = inputRenderers[evt.type]
    if (InputRenderer) {
      nodes.push(<InputRenderer key={evt.id} evt={evt} onTraceClick={onTraceClick} />)
      continue
    }

    // Decision request card — left side, collapsible, default expanded
    if (evt.type === 'tool.requested' && evt.payload?.name === 'request_human_decision') {
      const args = evt.payload?.arguments as Record<string, any> || {}
      const summary = (args.summary as string) || ''
      const context = (args.context as string) || ''
      const callId = (evt.payload?.call_id as string) || ''
      const reqId = 'hiw-' + callId
      const opts = (args.options as { id: string; label: string }[]) || []
      const isCollapsed = expandedContent.has(callId)
      nodes.push(
        <div key={evt.id} style={{ maxWidth: '70%', marginBottom: 12 }}>
          <div
            style={{
              border: '1px solid ' + (dark ? colors.decisionPending : 'rgba(180, 140, 60, 0.4)'),
              borderRadius: 8,
              padding: '10px 14px',
              fontSize: fontSizes.md,
              lineHeight: 1.6,
              color: colors.text,
            }}
          >
            <div
              onClick={() => toggleToolContent(callId)}
              style={{ cursor: 'pointer', userSelect: 'none', display: 'flex', alignItems: 'center', gap: 8 }}
            >
              <span style={{ color: colors.toolRequested, fontSize: fontSizes.base }}>Decision needed: {evt.worker_id} → {evt.target_worker_id || talkPartner}</span>
              <span style={{ color: colors.textDimmed, fontSize: fontSizes.sm }}>{isCollapsed ? '▸' : '▾'}</span>
              <span style={{ color: colors.textDimmed, fontSize: fontSizes.xs, marginLeft: 'auto' }}>{formatTime(evt.timestamp)}</span>
            </div>
            {!isCollapsed && (
              <div style={{ marginTop: 8, paddingTop: 8, borderTop: '1px solid ' + (dark ? 'rgba(180, 140, 60, 0.25)' : 'rgba(180, 140, 60, 0.15)') }}>
                <div style={{ fontWeight: 'bold', marginBottom: 4, fontSize: fontSizes.lg }}>{summary}</div>
                {context && <div style={{ color: colors.textDimmed, marginBottom: 8, fontSize: fontSizes.md }}>{context}</div>}
                {opts.length > 0 && (
                  <div style={{ display: 'flex', gap: 6, flexWrap: 'wrap' }}>
                    {opts.map((o: any) => (
                      <span
                        key={o.id}
                        onClick={() => makeDecision(reqId, o.id)}
                        className="decision-opt"
                        style={{
                          background: colors.bgLighter,
                          border: '1px solid ' + colors.borderLight,
                          borderRadius: 4,
                          padding: '4px 10px',
                          fontSize: fontSizes.sm,
                          color: colors.textDim,
                        }}
                      >
                        {o.label}
                      </span>
                    ))}
                  </div>
                )}
              </div>
            )}
          </div>
        </div>
      )
      continue
    }

    // Tool events: render individually in natural order
    if (isToolEvent(evt.type)) {
      const callId = toolCallId(evt)
      const isExpanded = expandedContent.has(callId)
      const content = toolContent(evt, isExpanded)
      const summary = toolSummary(evt)
      const statusColor = evt.type === 'tool.requested' ? colors.toolRequested
        : evt.type === 'tool.completed' ? colors.toolCompleted
        : evt.type === 'tool.failed' ? colors.toolFailed
        : colors.textDim

      const toolBorderColor = evt.type === 'tool.requested'
        ? (dark ? 'rgba(200, 138, 58, 0.35)' : 'rgba(200, 138, 58, 0.25)')
        : evt.type === 'tool.completed'
          ? (dark ? 'rgba(90, 138, 90, 0.35)' : 'rgba(90, 138, 90, 0.25)')
          : evt.type === 'tool.failed'
            ? (dark ? 'rgba(244, 67, 54, 0.35)' : 'rgba(244, 67, 54, 0.25)')
            : (dark ? 'rgba(136, 136, 136, 0.35)' : 'rgba(136, 136, 136, 0.25)')

      const toolLabel = evt.type === 'tool.requested' ? 'Tool Call'
        : evt.type === 'tool.completed' ? 'Tool Result'
        : evt.type === 'tool.failed' ? 'Tool Failed'
        : 'Tool Rejected'

      // Dim other tool events when one is expanded
      const isDimmed = anyToolExpanded && !isExpanded

      const tPad = compactMode ? '4px 8px' : '6px 12px'
      const tRadius = compactMode ? 6 : 8
      const tFontSize = compactMode ? fontSizes.xs : fontSizes.base

      nodes.push(
        <div key={evt.id} style={{ maxWidth: '70%', marginBottom: compactMode ? 8 : 12 }}>
          <div
            style={{
              border: '1px solid ' + toolBorderColor,
              borderRadius: tRadius,
              padding: tPad,
              fontSize: tFontSize,
              lineHeight: 1.5,
              color: colors.textDim,
              opacity: isDimmed ? 0.35 : 1,
              transition: 'opacity 0.15s',
            }}
          >
            <div
              onClick={() => toggleToolContent(callId)}
              style={{ cursor: 'pointer', userSelect: 'none', display: 'flex', alignItems: 'center', gap: 8 }}
            >
              <span style={{ color: isDimmed ? colors.textDimmed : statusColor, fontSize: tFontSize }}>{toolLabel} {summary}</span>
              {content && <span style={{ color: colors.textDimmed, fontSize: fontSizes.sm }}>{isExpanded ? '▾' : '▸'}</span>}
              <span style={{ color: colors.textDimmed, fontSize: fontSizes.xs, marginLeft: 'auto' }}>{formatTime(evt.timestamp)}</span>
            </div>
            {isExpanded && content && (
              <div style={{ marginTop: 6, paddingTop: 6, borderTop: '1px solid ' + (dark ? 'rgba(128,128,128,0.2)' : 'rgba(128,128,128,0.15)') }}>
                <pre
                  style={{
                    margin: 0,
                    padding: '6px 8px',
                    background: dark ? '#1e1e1e' : '#f5f5f5',
                    borderRadius: 4,
                    fontSize: fontSizes.sm,
                    lineHeight: 1.4,
                    overflowX: 'auto',
                    whiteSpace: 'pre-wrap',
                    wordBreak: 'break-word',
                    color: isDimmed ? colors.textDimmed : colors.text,
                  }}
                >
                  {content}
                </pre>
              </div>
            )}
          </div>
        </div>
      )
      continue
    }

    // Left-side events
    if (evt.type === 'reason.thinking') {
      const isDimmed = anyToolExpanded
      nodes.push(
        <div key={evt.id + '-thinking-' + thinkingExpanded} style={{ maxWidth: '70%', opacity: isDimmed ? 0.35 : 1, transition: 'opacity 0.15s' }}>
          <ThinkingBlock evt={evt} defaultExpanded={thinkingExpanded} compact={compactMode} />
        </div>
      )
    } else if (evt.type === 'reason.response') {
      const ref = findReferencedInput(events, evt)
      const isDimmed = anyToolExpanded
      nodes.push(
        <div key={evt.id} style={{ maxWidth: '70%', opacity: isDimmed ? 0.35 : 1, transition: 'opacity 0.15s' }}>
          <ResponseBlock evt={evt} quotedText={ref?.text} quotedWorker={ref?.workerId} />
        </div>
      )
    } else {
      nodes.push(
        <div key={evt.id} style={{ maxWidth: '70%', marginBottom: 12, fontSize: fontSizes.sm, color: colors.textDimmed }}>
          [{evt.type}] {formatEventPayload(evt)}
        </div>
      )
    }
  }

  if (nodes.length === 0) {
    return (
      <div style={{ display: 'flex', alignItems: 'center', justifyContent: 'center', height: '100%', color: colors.textDimmed }}>
        <p style={{ fontSize: fontSizes.md }}>No messages yet. Send a message below to start talking with <strong style={{ color: colors.textDim }}>{talkPartner}</strong>.</p>
      </div>
    )
  }

  return (
    <>
      {/* Header: always visible, not in scroll area */}
      <div style={{ marginTop: 24, marginBottom: 16, fontSize: fontSizes.md, color: colors.text, padding: '0 48px', display: 'flex', alignItems: 'center', gap: 16 }}>
        <span>
          Talking with <strong style={{ color: colors.textMuted }}>[{talkPartner}]</strong>
        </span>
        <div style={{ position: 'relative', marginLeft: 'auto' }}>
          <span
            onClick={() => setShowViewControl(!showViewControl)}
            style={{ cursor: 'pointer', fontSize: fontSizes.sm, color: colors.textDimmed, textDecoration: 'underline', textDecorationStyle: 'dotted' }}
          >
            View Settings
          </span>
          {showViewControl && (
            <div
              ref={viewControlRef}
              style={{
                position: 'absolute',
                top: '100%',
                right: 0,
                marginTop: 4,
                background: colors.bgLight,
                border: '1px solid ' + colors.border,
                borderRadius: 6,
                padding: '10px 14px',
                fontSize: fontSizes.sm,
                color: colors.text,
                zIndex: 100,
                whiteSpace: 'nowrap',
              }}
              onClick={(e) => e.stopPropagation()}
            >
              <div style={{ display: 'flex', alignItems: 'center', gap: 10 }}>
                <span style={{ color: colors.textDim }}>Expand Thinking</span>
                <div
                  onClick={() => setThinkingExpanded(!thinkingExpanded)}
                  style={{
                    width: 32,
                    height: 18,
                    borderRadius: 9,
                    background: thinkingExpanded ? colors.accent : colors.textDim,
                    cursor: 'pointer',
                    position: 'relative',
                    transition: 'background 0.15s',
                  }}
                >
                  <div
                    style={{
                      width: 14,
                      height: 14,
                      borderRadius: 7,
                      background: '#fff',
                      position: 'absolute',
                      top: 2,
                      left: thinkingExpanded ? 16 : 2,
                      transition: 'left 0.15s',
                    }}
                  />
                </div>
              </div>
              <div style={{ display: 'flex', alignItems: 'center', gap: 10, marginTop: 8 }}>
                <span style={{ color: colors.textDim }}>Compact Mode</span>
                <div
                  onClick={() => setCompactMode(!compactMode)}
                  style={{
                    width: 32,
                    height: 18,
                    borderRadius: 9,
                    background: compactMode ? colors.accent : colors.textDim,
                    cursor: 'pointer',
                    position: 'relative',
                    transition: 'background 0.15s',
                  }}
                >
                  <div
                    style={{
                      width: 14,
                      height: 14,
                      borderRadius: 7,
                      background: '#fff',
                      position: 'absolute',
                      top: 2,
                      left: compactMode ? 16 : 2,
                      transition: 'left 0.15s',
                    }}
                  />
                </div>
              </div>
            </div>
          )}
          {showViewControl && (
            <div
              style={{ position: 'fixed', top: 0, left: 0, right: 0, bottom: 0, zIndex: 99 }}
              onClick={() => setShowViewControl(false)}
            />
          )}
        </div>
      </div>
      <div ref={scrollRef} onScroll={handleScroll} style={{ flex: 1, overflowY: 'auto', overflowX: 'hidden', padding: '0 48px 80px' }}>
        {nodes}
      </div>
    </>
  )
}

// Also re-export helpers for consumers that may need them
export { formatTime, truncate }
