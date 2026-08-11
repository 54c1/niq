import { useState } from 'react'
import Markdown from 'react-markdown'
import remarkGfm from 'remark-gfm'
import { useTheme, fontSizes } from '../theme'
import { getContentText, formatTime } from './talk-utils'
import { makeMdComponents } from './MarkdownComponents'
import type { EventPayload } from '../types'

interface ThinkingBlockProps {
  evt: EventPayload
  defaultExpanded?: boolean
  compact?: boolean
}

export default function ThinkingBlock({ evt, defaultExpanded = true, compact = false }: ThinkingBlockProps) {
  const { dark, colors } = useTheme()
  const [collapsed, setCollapsed] = useState(!defaultExpanded)
  const text = getContentText(evt)

  const tPad = compact ? '4px 8px' : '6px 12px'
  const tRadius = compact ? 6 : 8
  const tFontSize = compact ? fontSizes.xs : fontSizes.base
  const tBorder = '1px solid ' + (dark ? '#9d7fa38c' : 'rgba(156, 39, 176, 0.35)')

  return (
    <div
      style={{
        marginBottom: compact ? 8 : 12,
        borderTop: tBorder,
        borderRight: tBorder,
        borderBottom: tBorder,
        borderLeft: tBorder,
        borderRadius: '0 ' + tRadius + 'px ' + tRadius + 'px ' + tRadius + 'px',
        padding: tPad,
        fontSize: tFontSize,
        lineHeight: 1.5,
        color: colors.textDim,
      }}
    >
      <div
        onClick={() => setCollapsed(!collapsed)}
        style={{ cursor: 'pointer', userSelect: 'none', display: 'flex', alignItems: 'center', gap: 8 }}
      >
        <span style={{ color: colors.eventType.reason, fontSize: tFontSize }}>Thinking</span>
        <span style={{ color: colors.textDimmed, fontSize: fontSizes.sm }}>{collapsed ? '▸' : '▾'} {text.length} chars</span>
        <span style={{ color: colors.textDimmed, fontSize: fontSizes.xs, marginLeft: 'auto' }}>{formatTime(evt.timestamp)}</span>
      </div>
      {!collapsed && text && (
        <div className="md-content" style={{ marginTop: 8, padding: '8px 0', borderTop: '1px solid ' + (dark ? 'rgba(156, 39, 176, 0.25)' : 'rgba(156, 39, 176, 0.2)') }}>
          <Markdown remarkPlugins={[remarkGfm]} components={makeMdComponents(dark, colors)}>{text}</Markdown>
        </div>
      )}
    </div>
  )
}
