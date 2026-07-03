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
