import Markdown from 'react-markdown'
import remarkGfm from 'remark-gfm'
import { Prism as SyntaxHighlighter } from 'react-syntax-highlighter'
import { vscDarkPlus } from 'react-syntax-highlighter/dist/esm/styles/prism'
import { useTheme, fontSizes } from '../theme'
import { type EventPayload } from '../types'
import { getTypeColor, summaryText, targetSummary, formatTime } from '../components/talk-utils'

const mdComponents = {
  code({ className, children, ...props }: any) {
    const match = /language-(\w+)/.exec(className || '')
    if (!match) {
      return <code className={className} {...props}>{children}</code>
    }
    return (
      <SyntaxHighlighter style={vscDarkPlus} language={match[1]} PreTag="div">
        {String(children).replace(/\n$/, '')}
      </SyntaxHighlighter>
    )
  },
}

interface EventRowProps {
  evt: EventPayload;
  expanded: boolean;
  onToggle: () => void;
  deliveries: Record<string, string[]>;
  workerTypes: Record<string, string>;
}

export default function EventRow({
  evt,
  expanded,
  onToggle,
  deliveries,
  workerTypes,
}: EventRowProps) {
  const { colors } = useTheme()
  const time = formatTime(evt.timestamp)
  const typeColor = getTypeColor(evt.type, colors)
  const isContentEvent =
    evt.type === "reason.thinking" ||
    evt.type === "reason.response" ||
    evt.type === "worker.input"
  const contentText = isContentEvent
    ? evt.type === "worker.input"
      ? (evt.payload?.text as string) || ""
      : Array.isArray(evt.payload?.content)
        ? (evt.payload.content as any[]).filter(Boolean).join("\n")
        : ""
    : ""

  const recipients = deliveries[evt.id] || evt.recipients

  const inputBlockStyle = evt.type === "worker.input"
    ? { background: colors.accentBg, border: "1px solid " + colors.accentBorder, color: colors.text }
    : evt.type === "reason.response"
      ? { background: colors.bgLight, border: "1px solid " + colors.border, color: colors.text }
      : { background: colors.bgLight, border: "1px dotted " + colors.border, color: colors.textDim, fontStyle: 'italic' as const }

  return (
    <div
      style={{
        marginBottom: 2,
        borderBottom: "1px solid " + colors.eventRowBorder,
        padding: "6px 0",
      }}
    >
      {/* Compact header — always visible */}
      <div
        onClick={onToggle}
        style={{
          cursor: "pointer",
          display: "grid",
          gridTemplateColumns: "200px 120px 150px 180px 1fr",
          gap: 8,
          alignItems: "baseline",
          userSelect: "none",
        }}
      >
        <span style={{ color: colors.eventRowTime, fontSize: fontSizes.md, whiteSpace: "nowrap" }}>{time}</span>
        <span style={{ color: colors.workerId, fontSize: fontSizes.md }}>
          {evt.worker_id}
        </span>
        <span style={{ color: typeColor, fontSize: fontSizes.md }}>[{evt.type}]</span>
        <span
          style={{
            overflow: "hidden",
            whiteSpace: "nowrap",
            color: recipients ? colors.eventRowTarget : evt.target_worker_id ? colors.eventRowTarget : colors.textDim,
            fontSize: recipients ? fontSizes.base : undefined,
          }}
        >
          {recipients ? (
            <span>
              → <span style={{ color: colors.eventRowTarget }}>{targetSummary(recipients.join(", "))}</span>
            </span>
          ) : evt.target_worker_id ? (
            <span style={{ color: colors.eventRowTarget }}>{targetSummary("→ " + evt.target_worker_id)}</span>
          ) : null}
        </span>
        <span
          style={{
            color: colors.eventRowSummary,
            overflow: "hidden",
            whiteSpace: "nowrap",
          }}
        >
          {summaryText(evt)}
        </span>
      </div>

      {/* Content row — rendered as Markdown, only when expanded */}
      {expanded && contentText && (
        <div
          className="md-content"
          style={{
            marginTop: 6,
            padding: "8px 12px",
            borderRadius: 6,
            fontSize: fontSizes.md,
            lineHeight: 1.6,
            ...inputBlockStyle,
          }}
        >
          <Markdown remarkPlugins={[remarkGfm]} components={mdComponents}>{contentText}</Markdown>
        </div>
      )}

      {/* Expanded detail — full width */}
      {expanded && (
        <div
          style={{
            marginTop: 8,
            padding: "10px 12px",
            background: colors.detailBg,
            borderRadius: 6,
            fontSize: fontSizes.base,
          }}
        >
          <div
            style={{
              display: "grid",
              gridTemplateColumns: "auto 1fr",
              gap: "4px 16px",
              alignItems: "baseline",
            }}
          >
            <DetailRow label="ID" value={evt.id} colors={colors} />
            <DetailRow label="TraceID" value={evt.trace_id || "(none)"} colors={colors} />
            <DetailRow label="Time" value={`${time} (${evt.timestamp})`} colors={colors} />
            <DetailRow
              label="Target"
              value={evt.target_worker_id || "(broadcast)"}
              colors={colors}
            />
            {recipients && (
              <DetailRow label="Delivered" value={recipients.join(", ")} colors={colors} />
            )}
          </div>
          <div
            style={{ marginTop: 8, borderTop: "1px solid " + colors.detailBorder, paddingTop: 8 }}
          >
            <div
              style={{
                color: colors.detailLabel,
                fontSize: fontSizes.base,
                marginBottom: 6,
                textTransform: "uppercase",
                letterSpacing: "0.5px",
                fontFamily: "monospace",
              }}
            >
              Payload
            </div>
            <pre
              style={{
                margin: 0,
                color: colors.detailValue,
                whiteSpace: "pre-wrap",
                wordBreak: "break-all",
                fontSize: fontSizes.base,
                lineHeight: 1.5,
                fontFamily: "monospace",
              }}
            >
              {JSON.stringify(evt.payload, null, 2)}
            </pre>
          </div>
        </div>
      )}
    </div>
  );
}

// ── DetailRow ──

function DetailRow({ label, value, colors }: { label: string; value: string; colors: import('../theme').Palette }) {
  return (
    <>
      <span style={{ color: colors.detailLabel, fontSize: fontSizes.base }}>{label}</span>
      <span style={{ color: colors.detailValue, fontSize: fontSizes.base, wordBreak: "break-all" }}>
        {value}
      </span>
    </>
  )
}