import { useState, useRef, useEffect, useMemo } from 'react'
import { useTheme, fontSizes } from '../theme'
import type { WorkerInfo } from '../types'

interface TalkInputProps {
  talkPartner: string
  input: string
  inputMode: string
  onInputChange: (v: string) => void
  onSend: () => void
  onAbort: () => void
  onModeChange: (m: string) => void
  workers: WorkerInfo[]
  mentionKey: number
}

export default function TalkInput({ talkPartner, input, inputMode, onInputChange, onSend, onAbort, onModeChange, workers, mentionKey }: TalkInputProps) {
  const { colors } = useTheme()
  const textareaRef = useRef<HTMLTextAreaElement>(null)
  const [showMentions, setShowMentions] = useState(false)
  const [mentionQuery, setMentionQuery] = useState('')
  const [mentionIndex, setMentionIndex] = useState(0)

  const reasonWorkers = useMemo(() => workers.filter(w => w.type === 'reason'), [workers])

  const handleChange = (e: React.ChangeEvent<HTMLTextAreaElement>) => {
    const val = e.target.value
    onInputChange(val)

    // Detect if we're typing an @mention
    const cursorPos = e.target.selectionStart
    const beforeCursor = val.slice(0, cursorPos)
    const atMatch = beforeCursor.match(/@(\w*)$/)
    if (atMatch) {
      setShowMentions(true)
      setMentionQuery(atMatch[1].toLowerCase())
      setMentionIndex(0)
    } else {
      setShowMentions(false)
    }
  }

  const selectMention = (id: string) => {
    const cursorPos = textareaRef.current?.selectionStart ?? input.length
    const beforeCursor = input.slice(0, cursorPos)
    const afterCursor = input.slice(cursorPos)
    const atMatch = beforeCursor.match(/^(.*)@\w*$/)
    if (atMatch) {
      const newVal = atMatch[1] + '@' + id + ' ' + afterCursor
      onInputChange(newVal)
      // Move cursor after the inserted mention
      requestAnimationFrame(() => {
        const ta = textareaRef.current
        if (ta) {
          const pos = atMatch[1].length + id.length + 2
          ta.setSelectionRange(pos, pos)
          ta.focus()
        }
      })
    }
    setShowMentions(false)
  }

  const handleKeyDown = (e: React.KeyboardEvent<HTMLTextAreaElement>) => {
    if (showMentions) {
      const filtered = reasonWorkers.filter(w => w.id.toLowerCase().includes(mentionQuery))
      if (e.key === 'ArrowDown') {
        e.preventDefault()
        setMentionIndex(i => Math.min(i + 1, filtered.length - 1))
        return
      }
      if (e.key === 'ArrowUp') {
        e.preventDefault()
        setMentionIndex(i => Math.max(i - 1, 0))
        return
      }
      if (e.key === 'Enter' || e.key === 'Tab') {
        if (filtered.length > 0) {
          e.preventDefault()
          selectMention(filtered[mentionIndex].id)
          return
        }
      }
      if (e.key === 'Escape') {
        setShowMentions(false)
        return
      }
    }
    if (e.key === 'Enter' && !e.shiftKey) {
      e.preventDefault()
      onSend()
    }
  }

  // Parse current @mention target for display
  const currentTarget = useMemo(() => {
    const m = input.match(/^@(\S+)\s/)
    if (m) {
      const w = reasonWorkers.find(r => r.id === m[1])
      return w ? w.id : null
    }
    return null
  }, [input, reasonWorkers])

  // Close mentions on blur
  useEffect(() => {
    const handler = () => setShowMentions(false)
    window.addEventListener('click', handler)
    return () => window.removeEventListener('click', handler)
  }, [])

  // Focus the textarea when a mention is triggered from outside
  useEffect(() => {
    if (mentionKey > 0) {
      textareaRef.current?.focus()
    }
  }, [mentionKey])

  const filtered = showMentions ? reasonWorkers.filter(w => w.id.toLowerCase().includes(mentionQuery)) : []

  return (
    <div style={{ padding: '12px 48px', borderTop: '1px solid ' + colors.border, position: 'relative' }}>
      {/* Target indicator */}
      {currentTarget && (
        <div style={{ fontSize: fontSizes.xs, color: colors.textDimmed, marginBottom: 4 }}>
          → <span style={{ color: colors.accent, fontWeight: 'bold' }}>{currentTarget}</span>
        </div>
      )}

      {/* Mention dropdown */}
      {showMentions && filtered.length > 0 && (
        <div
          style={{
            position: 'absolute',
            bottom: '100%',
            left: 48,
            right: 48,
            background: colors.bgLight,
            border: '1px solid ' + colors.border,
            borderRadius: 6,
            maxHeight: 160,
            overflowY: 'auto',
            zIndex: 100,
            boxShadow: '0 4px 12px rgba(0,0,0,0.15)',
          }}
        >
          {filtered.map((w, i) => (
            <div
              key={w.id}
              onClick={() => selectMention(w.id)}
              style={{
                padding: '6px 12px',
                cursor: 'pointer',
                fontSize: fontSizes.sm,
                color: i === mentionIndex ? colors.accent : colors.textDim,
                background: i === mentionIndex ? (colors.bgChip || 'rgba(128,128,128,0.1)') : 'transparent',
                fontFamily: 'monospace',
              }}
            >
              {w.id}
              <span style={{ color: colors.textDimmed, fontStyle: 'italic', marginLeft: 8 }}>{w.type}</span>
            </div>
          ))}
        </div>
      )}

      <textarea
        ref={textareaRef}
        value={input}
        onChange={handleChange}
        onKeyDown={handleKeyDown}
        placeholder='Type a message... (@ to mention a worker, Shift+Enter for new line)'
        rows={3}
        style={{
          width: '100%',
          background: 'transparent',
          color: colors.text,
          border: 'none',
          outline: 'none',
          padding: '8px 0',
          fontSize: 14,
          fontFamily: 'monospace',
          resize: 'none',
          lineHeight: 1.5,
          boxSizing: 'border-box',
        }}
      />
      <div style={{ display: 'flex', gap: 12, justifyContent: 'flex-end', alignItems: 'center' }}>
        <select
          value={inputMode}
          onChange={(e) => onModeChange(e.target.value)}
          style={{
            background: 'transparent',
            color: colors.textDim,
            border: 'none',
            outline: 'none',
            fontSize: 12,
            fontFamily: 'monospace',
            cursor: 'pointer',
          }}
        >
          <option value="default">default</option>
          <option value="append">append</option>
        </select>
        <button
          onClick={onAbort}
          className="btn-stop"
          style={{
            background: 'none',
            color: colors.textDim,
            border: 'none',
            padding: '4px 12px',
            borderRadius: 4,
            cursor: 'pointer',
            fontSize: 13,
            fontFamily: 'monospace',
          }}
        >
          Stop
        </button>
        <button
          onClick={onSend}
          className="btn-send"
          style={{
            background: 'none',
            color: colors.accent,
            border: 'none',
            padding: '4px 12px',
            borderRadius: 4,
            cursor: 'pointer',
            fontSize: 13,
            fontWeight: 'bold',
            fontFamily: 'monospace',
          }}
        >
          Send
        </button>
      </div>
    </div>
  )
}