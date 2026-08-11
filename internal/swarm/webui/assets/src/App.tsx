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
  const [sending, setSending] = useState(false)
  const [mentionKey, setMentionKey] = useState(0)
  const [filterWorker, setFilterWorker] = useState('')
  const [expanded, setExpanded] = useState<Set<string>>(new Set())
  const [deliveries, setDeliveries] = useState<Record<string, string[]>>({})
  const [talkWorkers, setTalkWorkers] = useState<Set<string>>(new Set())
  const [traceFilter, setTraceFilter] = useState('')

  const eventsRef = useRef<EventPayload[]>([])
  const deliveriesRef = useRef<Record<string, string[]>>({})
  const listRef = useRef<HTMLDivElement>(null)
  const autoScrollRef = useRef(true)
  const sentinelRef = useRef<HTMLDivElement>(null)

  // ── SSE: always fetch all events, frontend filters ──
  const sseKey = view === 'talk' ? 'talk-all' : 'events-' + filterWorker

  useEffect(() => {
    const params = new URLSearchParams()
    if (view === 'events' && traceFilter) {
      params.set('trace', traceFilter)
    } else if (view === 'events' && filterWorker) {
      params.set('worker', filterWorker)
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

  // ── Callbacks ──
  const sendMessage = useCallback(() => {
    if (!input.trim() || sending) return
    setSending(true)
    // Parse @mention for targeting specific worker.
    // Format: "@worker_name message" sends to that worker.
    let msgTarget = ''
    let msgText = input
    const mentionMatch = input.match(/^@(\S+)\s+(.*)$/s)
    if (mentionMatch) {
      const mentioned = mentionMatch[1]
      const reasonWorkers = workers.filter(w => w.type === 'reason')
      if (reasonWorkers.some(r => r.id === mentioned)) {
        msgTarget = mentioned
        msgText = mentionMatch[2]
      }
    }
    if (!msgTarget) {
      // No @mention found: target the first selected reason worker, or broadcast.
      const reasonWorkers = workers.filter(w => w.type === 'reason')
      const selectedReasons = [...talkWorkers].filter(id => reasonWorkers.some(r => r.id === id))
      msgTarget = selectedReasons.length > 0 ? selectedReasons[0] : ''
    }
    sendInput(msgText, msgTarget, inputMode).then(() => {
      setInput('')
      setSending(false)
    }).catch(() => {
      setSending(false)
    })
  }, [input, view, talkWorkers, inputMode, sending, workers])

  const handleAbort = useCallback(() => {
    const reasonWorkers = workers.filter(w => w.type === 'reason')
    if (reasonWorkers.length > 0) {
      abortWorker(reasonWorkers[0].id)
    }
  }, [workers])

  const toggleWorker = useCallback((id: string) => {
    setTalkWorkers(prev => {
      const next = new Set(prev)
      if (next.has(id)) next.delete(id)
      else next.add(id)
      return next
    })
  }, [])

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
    const worker = view === 'events' ? filterWorker : ''
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
  }, [events, view, filterWorker, traceFilter])

  // ── Events list auto-scroll ──
  useEffect(() => {
    if (autoScrollRef.current && listRef.current) {
      listRef.current.scrollTo({ top: listRef.current.scrollHeight, behavior: 'smooth' })
    }
  }, [events])

  // Auto-load more events when scrolling to top.
  useEffect(() => {
    if (view !== 'events' || events.length === 0) return
    const el = sentinelRef.current
    if (!el) return
    const observer = new IntersectionObserver((entries) => {
      if (entries[0].isIntersecting) {
        loadMore()
      }
    }, { rootMargin: '200px 0px' })
    observer.observe(el)
    return () => observer.disconnect()
  }, [view, events.length, loadMore])

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
        talkWorkers={talkWorkers}
        onToggleWorker={toggleWorker}
      />

      <div style={{ flex: 1, display: 'flex', flexDirection: 'column' }}>
        {view === 'talk' ? (
          <>
            <TalkView
              events={events}
              talkWorkers={talkWorkers}
              onTraceClick={handleTraceClick}
              onLoadMore={loadMore}
              onMention={(id) => { setInput(prev => prev + '@' + id + ' '); setMentionKey(k => k + 1) }}
              deliveries={deliveries}
              decisions={decisions}
              makeDecision={makeDecision}
            />

            <TalkInput
              talkPartner={''}
              input={input}
              inputMode={inputMode}
              onInputChange={setInput}
              onSend={sendMessage}
              onAbort={handleAbort}
              onModeChange={setInputMode}
              workers={workers}
              mentionKey={mentionKey}
            />
          </>
        ) : view === 'events' ? (
          <>
            <div style={{ marginTop: 40, marginBottom: 16, fontSize: fontSizes.lg, color: colors.text, padding: '0 48px' }}>
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
              {events.length > 0 && <div ref={sentinelRef} style={{ height: 1 }} />}
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