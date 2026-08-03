import { useTheme } from '../theme'

interface TalkInputProps {
  talkPartner: string
  input: string
  inputMode: string
  onInputChange: (v: string) => void
  onSend: () => void
  onAbort: () => void
  onModeChange: (m: string) => void
}

export default function TalkInput({ talkPartner, input, inputMode, onInputChange, onSend, onAbort, onModeChange }: TalkInputProps) {
  const { colors } = useTheme()

  const handleKeyDown = (e: React.KeyboardEvent<HTMLTextAreaElement>) => {
    if (e.key === 'Enter' && !e.shiftKey) {
      e.preventDefault()
      onSend()
    }
  }

  return (
    <div style={{ padding: '12px 48px', borderTop: '1px solid ' + colors.border }}>
      <textarea
        value={input}
        onChange={(e) => onInputChange(e.target.value)}
        onKeyDown={handleKeyDown}
        placeholder="Type a message... (Shift+Enter for new line)"
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
          <option value="direct">direct</option>
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