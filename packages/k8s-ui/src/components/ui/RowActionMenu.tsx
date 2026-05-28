import { Fragment, useEffect, useRef, useState, type ComponentType } from 'react'
import { Loader2, MoreVertical } from 'lucide-react'
import { clsx } from 'clsx'
import { Tooltip } from './Tooltip'

export interface RowActionItem {
  key: string
  label: string
  icon: ComponentType<{ className?: string }>
  onClick: () => void
  disabled?: boolean
  disabledReason?: string
  pending?: boolean
  danger?: boolean
  /** Render a horizontal divider above this item. */
  divider?: boolean
}

interface RowActionMenuProps {
  items: RowActionItem[]
  ariaLabel?: string
  /** Compact button variant (default: true) — sized for table-row anchoring. */
  compact?: boolean
}

export function RowActionMenu({ items, ariaLabel = 'Row actions', compact = true }: RowActionMenuProps) {
  const [open, setOpen] = useState(false)
  const ref = useRef<HTMLDivElement>(null)

  useEffect(() => {
    if (!open) return
    const onDown = (e: MouseEvent) => {
      if (ref.current && !ref.current.contains(e.target as Node)) setOpen(false)
    }
    const onKey = (e: KeyboardEvent) => {
      if (e.key === 'Escape') setOpen(false)
    }
    document.addEventListener('mousedown', onDown)
    document.addEventListener('keydown', onKey)
    return () => {
      document.removeEventListener('mousedown', onDown)
      document.removeEventListener('keydown', onKey)
    }
  }, [open])

  const triggerSize = compact ? 'p-1' : 'p-1.5'
  const iconSize = compact ? 'h-4 w-4' : 'h-5 w-5'

  return (
    <div ref={ref} className="relative inline-block">
      <button
        type="button"
        aria-label={ariaLabel}
        aria-haspopup="menu"
        aria-expanded={open}
        onClick={(e) => {
          e.stopPropagation()
          setOpen((v) => !v)
        }}
        className={clsx(
          'rounded text-theme-text-tertiary hover:bg-theme-hover hover:text-theme-text-primary',
          triggerSize,
        )}
      >
        <MoreVertical className={iconSize} />
      </button>
      {open && (
        <div
          role="menu"
          className="absolute right-0 top-full z-50 mt-1 min-w-[180px] rounded-lg border border-theme-border bg-theme-surface py-1 shadow-xl"
          onClick={(e) => e.stopPropagation()}
        >
          {items.map((item) => {
            const Icon = item.icon
            const content = (
              <button
                type="button"
                role="menuitem"
                disabled={item.disabled || item.pending}
                onClick={(e) => {
                  e.stopPropagation()
                  if (item.disabled || item.pending) return
                  item.onClick()
                  setOpen(false)
                }}
                className={clsx(
                  'flex w-full items-center gap-2 px-3 py-1.5 text-left text-xs transition-colors',
                  item.disabled || item.pending
                    ? 'cursor-not-allowed text-theme-text-tertiary'
                    : item.danger
                    ? 'text-red-500 hover:bg-theme-hover hover:text-red-400'
                    : 'text-theme-text-secondary hover:bg-theme-hover hover:text-theme-text-primary',
                )}
              >
                {item.pending ? (
                  <Loader2 className="h-3.5 w-3.5 shrink-0 animate-spin" />
                ) : (
                  <Icon className="h-3.5 w-3.5 shrink-0" />
                )}
                <span className="truncate">{item.label}</span>
              </button>
            )
            return (
              <Fragment key={item.key}>
                {item.divider && <div className="my-1 h-px bg-theme-border" />}
                {item.disabled && item.disabledReason ? (
                  <Tooltip content={item.disabledReason} position="left">
                    {content}
                  </Tooltip>
                ) : (
                  content
                )}
              </Fragment>
            )
          })}
        </div>
      )}
    </div>
  )
}
