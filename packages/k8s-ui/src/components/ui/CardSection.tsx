import { Fragment, type ComponentType, type ReactNode } from 'react'

// =============================================================================
// CARD SECTION PRIMITIVES — the shared "Sectioned + icons" vocabulary (SKY-1057)
// Used by the Issue card (IssuesView) and the Check card (ChecksView) so both
// read as one product: ONE eyebrow style, an icon gutter per section, and a
// single body contrast floor (never lighter than the secondary text token —
// the grey-on-grey fix). Severity is the only status color; section icons carry
// quiet role hues (what's-wrong = amber, fix = emerald, neutral = grey).
// =============================================================================

// Section role → eyebrow + icon hue. Body text always uses the secondary token
// (the contrast floor), regardless of tone.
export type CardSectionTone = 'neutral' | 'warn' | 'fix'

const SECTION_TONE_CLASS: Record<CardSectionTone, string> = {
  // Eyebrow grey — the default, for neutral sections (Why it matters, Raw error,
  // Affected resources, Context, Subject).
  neutral: 'text-theme-text-tertiary',
  // What's wrong — amber, the diagnosis beat.
  warn: 'text-amber-700 dark:text-amber-300',
  // Next step / How to fix — emerald, the action beat (matches brand links).
  fix: 'text-emerald-700 dark:text-emerald-400',
}

// The single eyebrow style: 11px / 600 / uppercase, with an icon in a fixed
// gutter so every section's body aligns under the same left edge.
export function CardSection({
  icon: Icon,
  label,
  tone = 'neutral',
  labelExtra,
  children,
}: {
  icon: ComponentType<{ className?: string }>
  label: string
  tone?: CardSectionTone
  /** Optional trailing eyebrow text (e.g. "· retried 5×"), rendered muted. */
  labelExtra?: ReactNode
  children: ReactNode
}) {
  const toneClass = SECTION_TONE_CLASS[tone]
  return (
    <section className="flex gap-2.5">
      <Icon className={`mt-px h-4 w-4 shrink-0 ${toneClass}`} aria-hidden />
      <div className="flex min-w-0 flex-1 flex-col gap-1">
        <h4 className={`text-[11px] font-semibold uppercase tracking-[0.06em] ${toneClass}`}>
          {label}
          {labelExtra ? <span className="font-medium text-theme-text-tertiary"> {labelExtra}</span> : null}
        </h4>
        {children}
      </div>
    </section>
  )
}

// Body paragraph at the contrast floor — 14px, secondary token, never lighter.
export function CardBody({ children, className }: { children: ReactNode; className?: string }) {
  return <p className={`text-sm leading-relaxed text-theme-text-secondary ${className ?? ''}`}>{children}</p>
}

// The dark terminal block for raw error output. Intentionally dark in BOTH
// themes (it's a console surface, not chrome) — so it gets an explicit border
// that stays visible on a dark app surface, and a light-mono body for contrast.
export function TerminalBlock({ label, children }: { label?: ReactNode; children: ReactNode }) {
  return (
    <div className="overflow-hidden rounded-lg border border-slate-800 bg-[#0f172a] dark:border-white/10">
      {label ? (
        <div className="border-b border-white/10 px-3 py-1.5 text-[10px] font-semibold uppercase tracking-[0.06em] text-slate-400">
          {label}
        </div>
      ) : null}
      <pre className="overflow-x-auto whitespace-pre-wrap break-words px-3 py-2.5 font-mono text-xs leading-relaxed text-slate-300">
        {children}
      </pre>
    </div>
  )
}

// The neutral category chip — drops the per-category hue so severity is the only
// status color (SKY-1057 hard rule). Self-contained (don't pair with badge-sm):
// 11px / 600, 2px×8px padding, no border, neutral grey — matches the prototype.
export const NEUTRAL_CHIP_CLASS =
  'inline-flex items-center rounded-md px-2 py-0.5 text-[11px] font-semibold leading-tight bg-theme-elevated text-theme-text-secondary'

// Mono inline-code chip. Used by renderProse for `backtick` spans in prose.
const INLINE_CODE_CLASS =
  'rounded bg-theme-elevated px-1 py-0.5 font-mono text-xs text-theme-text-primary ring-1 ring-theme-border'

// Render a prose string, turning markdown `inline-code` spans into mono chips.
// Plain-text passthrough when the string carries no backticks — so it's a no-op
// for today's plain catalog copy and lights up automatically if backtick markup
// is added later (SKY-1057 decision: prose now, parser ready).
export function renderProse(text: string | undefined | null): ReactNode {
  if (!text) return null
  if (!text.includes('`')) return text
  const parts = text.split(/(`[^`]+`)/g)
  return parts.map((part, i) => {
    if (part.length > 1 && part.startsWith('`') && part.endsWith('`')) {
      return (
        <code key={i} className={INLINE_CODE_CLASS}>
          {part.slice(1, -1)}
        </code>
      )
    }
    return <Fragment key={i}>{part}</Fragment>
  })
}
