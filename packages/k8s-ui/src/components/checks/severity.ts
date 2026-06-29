import type { CheckSeverity } from './types'
import { BADGE_SEVERITY_COLORS as sev } from '../ui/Badge'
import {
  TONE_FILL_CLASS,
  TONE_HEADER_BAND_CLASS,
  TONE_RAIL_CLASS,
  TONE_SOLID_CLASS,
  TONE_TEXT_CLASS,
  type SeverityTone,
} from '../ui/severity-tone'

// The visual language for the 4-tier Checks severity ladder. One hue per tier:
// red=critical, orange=high, amber=medium, neutral=low — read the queue's left
// rail top-to-bottom and severity is obvious without reading a word. The actual
// color strings are shared with the Issue card via the tone module; here we only
// map each tier onto its tone.
const CHECK_SEVERITY_TONE: Record<CheckSeverity, SeverityTone> = {
  critical: 'red',
  high: 'orange',
  medium: 'amber',
  low: 'slate',
}

const byTone = <T,>(toneMap: Record<SeverityTone, T>): Record<CheckSeverity, T> => ({
  critical: toneMap[CHECK_SEVERITY_TONE.critical],
  high: toneMap[CHECK_SEVERITY_TONE.high],
  medium: toneMap[CHECK_SEVERITY_TONE.medium],
  low: toneMap[CHECK_SEVERITY_TONE.low],
})

export const SEVERITY_LABEL: Record<CheckSeverity, string> = {
  critical: 'Critical',
  high: 'High',
  medium: 'Medium',
  low: 'Low',
}

// Pill badge — the loud, explicit severity signal on rows + drawer header.
// Uses the canonical Badge severity tones (BADGE_SEVERITY_COLORS) so the queue's
// severity pills read identically to status badges everywhere else (rendered
// with `badge-sm`).
export const SEVERITY_BADGE_CLASS: Record<CheckSeverity, string> = {
  critical: sev.error,
  high: sev.alert,
  medium: sev.warning,
  low: sev.neutral,
}

export const SEVERITY_FILL_CLASS = byTone(TONE_FILL_CLASS)
export const SEVERITY_TEXT_CLASS = byTone(TONE_TEXT_CLASS)
export const SEVERITY_RAIL_CLASS = byTone(TONE_RAIL_CLASS)
export const SEVERITY_SOLID_CLASS = byTone(TONE_SOLID_CLASS)
export const SEVERITY_HEADER_BAND_CLASS = byTone(TONE_HEADER_BAND_CLASS)

// Category accent — a quiet tag (severity is the loud one). Security is the
// headline beat, so it gets the most distinct hue.
const CATEGORY_BADGE_CLASS: Record<string, string> = {
  Security: 'bg-violet-50 text-violet-700 ring-1 ring-violet-200 dark:bg-violet-950/40 dark:text-violet-300 dark:ring-violet-900',
  Reliability: 'bg-sky-50 text-sky-700 ring-1 ring-sky-200 dark:bg-sky-950/40 dark:text-sky-300 dark:ring-sky-900',
  Efficiency: 'bg-teal-50 text-teal-700 ring-1 ring-teal-200 dark:bg-teal-950/40 dark:text-teal-300 dark:ring-teal-900',
}

export function categoryBadgeClass(category: string): string {
  return (
    CATEGORY_BADGE_CLASS[category] ??
    'bg-theme-elevated text-theme-text-secondary ring-1 ring-theme-border'
  )
}
