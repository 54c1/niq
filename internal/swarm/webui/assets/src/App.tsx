import { useEffect, useState, useRef, useCallback, useMemo } from 'react'
import Sidebar from './views/Sidebar'
import EventRow from './views/EventRow'
import TalkView from './views/TalkView'
import Decisions from './views/Decisions'
import TalkInput from './components/TalkInput'
import { useTheme, fontSizes } from './theme'
import { usePolling } from './hooks/usePolling'
import { sendInput, abortWorker, makeDecision as apiMakeDecision, fetchWorkers, fetchDecisions, loadEventsBefore } from './services/api'
import type { EventPayload, Decision, WorkerInfo } from './types'

type ViewMode = 'talk' | 'events' | 'decisions'

export default function App() {
  const { dark, colors } = useTheme()

  // ── Theme sync ──
  useEffect(() => {
    document.documentElement.style.background = colors.bg
    document.body.style.background = colors.bg
    const root = document.getElementById('root')
    if (root) root.style.background = colors.bg
  }, [colors.bg])

  // ── State ──
  const [events, setEvents] = useState<EventPayload[]>([])
  const [workers, setWorkers] = useState<WorkerInfo[]>([])
  const [decisions, setDecisions] = useState<Decision[]>([])
  const [view, setView] = useState<ViewMode>('talk')
  const [input, setInput] = useState('')
  const [inputMode, setInputMode] = useState('default')
  const [filterWorker, setFilterWorker] = useState('')
  const [expanded, setExpanded] = useState<Set<string>>(new Set())
  const [deliveries, setDeliveries] = useState<Record<string, string[]>>({})
  const [talkPartner, setTalkPartner] = useState('')
  const [traceFilter, setTraceFilter] = useState('')

  const initialSelectDone = useRef(false)
  const eventsRef = useRef<EventPayload[]>([])
  const deliveriesRef = useRef<Record<string, string[]>>({})
  const listRef = useRef<HTMLDivElement>(null)
  const autoScrollRef = useRef(true)

  // ── SSE ──
  const effectiveWorker = view === 'talk' ? '' : filterWorker
  const sseKey = view === 'talk' ? 'talk-' + talkPartner : 'events-' + filterWorker

  useEffect(() => {
    const params = new URLSearchParams()
    if (view === 'talk' && talkPartner) {
      params.set('worker', talkPartner)
    } else if (view === 'events' && traceFilter) {
      params.set('trace', traceFilter)
    } else if (effectiveWorker && view !== 'talk') {
      params.set('worker', effectiveWorker)
    }
    const url = `/api/stream?${params}`
    setEvents([])
    eventsRef.current = []
    setDeliveries({})
    deliveriesRef.current = {}
    const es = new EventSource(url)
    es.onmessage = (msg) => {
      const evt = JSON.parse(msg.data) as EventPayload
      if (evt.type === 'event.delivered') {
        const eventId = evt.payload?.event_id as string | undefined
        const recipients = evt.payload?.recipients as string[] | undefined
        if (eventId && recipients) {
          deliveriesRef.current = { ...deliveriesRef.current, [eventId]: recipients }
          setDeliveries(deliveriesRef.current)
        }
        return
      }
      eventsRef.current = [...eventsRef.current.slice(-200), evt]
      setEvents(eventsRef.current)
    }
    return () => es.close()
  }, [sseKey, traceFilter])

  // ── Polling ──
  usePolling<WorkerInfo[]>('/api/workers', 5000, setWorkers)
  usePolling<Decision[]>('/api/decisions', 3000, setDecisions)

  // ── Auto-select first reason worker ──
  useEffect(() => {
    if (!initialSelectDone.current && view === 'talk' && !talkPartner && workers.length > 0) {
      const firstReason = workers.find(w => w.type === 'reason')
      if (firstReason) {
        setTalkPartner(firstReason.id)
        initialSelectDone.current = true
      }
    }
  }, [view, talkPartner, workers])

  // ── Callbacks ──
  const sendMessage = useCallback(() => {
    if (!input.trim()) return
    const msgTarget = view === 'talk' ? talkPartner : ''
    sendInput(input, msgTarget, inputMode)
    setInput('')
  }, [input, view, talkPartner, inputMode])

  const handleAbort = useCallback(() => {
    abortWorker(talkPartner)
  }, [talkPartner])

  const toggleExpand = useCallback((id: string) => {
    setExpanded((prev) => {
      const next = new Set(prev)
      if (next.has(id)) next.delete(id)
      else next.add(id)
      return next
    })
  }, [])

  const makeDecision = useCallback(
    (reqID: string, decision: string) => {
      apiMakeDecision(reqID, decision)
    },
    [],
  )

  const handleTraceClick = useCallback((traceId: string) => {
    setTraceFilter(traceId)
    setView('events')
  }, [])

  const clearTraceFilter = useCallback(() => {
    setTraceFilter('')
  }, [])

  // ── Load more older events ──
  const loadMore = useCallback(async () => {
    if (events.length === 0) return
    const oldestId = events[0].id
    const worker = view === 'talk' ? talkPartner : (view === 'events' ? filterWorker : '')
    const trace = view === 'events' ? traceFilter : ''
    try {
      const older = await loadEventsBefore(oldestId, 50, worker, trace)
      if (older.length === 0) return
      const filtered = older.filter((e: any) => e.type !== 'event.delivered')
      if (filtered.length === 0) return
      const prepend = filtered.reverse()
      eventsRef.current = [...prepend, ...eventsRef.current]
      setEvents(eventsRef.current)
    } catch {}
  }, [events, view, filterWorker, traceFilter, talkPartner])

  // ── Events list auto-scroll ──
  useEffect(() => {
    if (autoScrollRef.current && listRef.current) {
      listRef.current.scrollTo({ top: listRef.current.scrollHeight, behavior: 'smooth' })
    }
  }, [events])

  const handleScroll = useCallback(() => {
    const el = listRef.current
    if (!el) return
    const atBottom = el.scrollHeight - el.scrollTop - el.clientHeight < 50
    autoScrollRef.current = atBottom
  }, [])

  const workerTypes = useMemo(() => {
    const map: Record<string, string> = {}
    for (const w of workers) {
      map[w.id] = w.type
    }
    return map
  }, [workers])

  // ── Render ──
  return (
    <div data-theme={dark ? 'dark' : 'light'} style={{ display: 'flex', height: '100vh', fontFamily: 'monospace', color: colors.text, background: colors.bg }}>
      <Sidebar
        view={view}
        setView={setView}
        decisions={decisions}
        filterWorker={filterWorker}
        setFilterWorker={setFilterWorker}
        workers={workers}
        talkPartner={talkPartner}
        setTalkPartner={setTalkPartner}
      />

      <div style={{ flex: 1, display: 'flex', flexDirection: 'column' }}>
        {view === 'talk' ? (
          <>
            <TalkView
              events={events}
              talkPartner={talkPartner}
              onTraceClick={handleTraceClick}
              onLoadMore={loadMore}
              deliveries={deliveries}
              decisions={decisions}
              makeDecision={makeDecision}
            />

            {talkPartner && (
              <TalkInput
                talkPartner={talkPartner}
                input={input}
                inputMode={inputMode}
                onInputChange={setInput}
                onSend={sendMessage}
                onAbort={handleAbort}
                onModeChange={setInputMode}
              />
            )}
          </>
        ) : view === 'events' ? (
          <>
            <div style={{ marginTop: 24, marginBottom: 16, fontSize: fontSizes.md, color: colors.text, padding: '0 48px' }}>
              Events
              {filterWorker && <span> <strong style={{ color: colors.textMuted }}>[{filterWorker}]</strong> sent / received</span>}
              {traceFilter && <span> — trace <strong style={{ color: colors.textMuted }}>{traceFilter}</strong></span>}
              {traceFilter && (
                <span
                  onClick={clearTraceFilter}
                  style={{ cursor: 'pointer', color: colors.textDimmed, textDecoration: 'underline', marginLeft: 12, fontSize: fontSizes.sm }}
                >
                  Clear
                </span>
              )}
            </div>

            <div ref={listRef} onScroll={handleScroll} style={{ flex: 1, overflowY: 'auto', fontSize: fontSizes.md, padding: '0 48px 20px 48px' }}>
              {events.length > 0 && (
                <div key="load-more" style={{ textAlign: 'center', padding: '12px 0' }}>
                  <span
                    onClick={loadMore}
                    style={{ cursor: 'pointer', fontSize: fontSizes.sm, color: colors.textDim, textDecoration: 'underline', textDecorationStyle: 'dotted' }}
                  >
                    load earlier events
                  </span>
                </div>
              )}
              {events.map((evt) => (
                <EventRow key={evt.id} evt={evt} expanded={expanded.has(evt.id)} onToggle={() => toggleExpand(evt.id)} deliveries={deliveries} workerTypes={workerTypes} />
              ))}
            </div>
          </>
        ) : (
          <div style={{ flex: 1, overflowY: 'auto', padding: 30 }}>
            <Decisions decisions={decisions} makeDecision={makeDecision} />
          </div>
        )}
      </div>
    </div>
  )
}