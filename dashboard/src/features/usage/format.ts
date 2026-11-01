/**
 * Number and time formatting for the usage pages.
 */
import type { Meter } from '@/services/api/store';

const plain = new Intl.NumberFormat('en', { maximumFractionDigits: 2 });

/** A meter value in its own unit. */
export const formatMeterValue = (value: number | null | undefined, unit: string): string => {
  if (value === null || value === undefined || !Number.isFinite(value)) return '—';
  switch (unit) {
    case 'percent':
      return `${plain.format(value)}%`;
    case 'usd':
      return `$${plain.format(value)}`;
    default:
      return plain.format(value);
  }
};

/** A meter's used share from 0 to 1, from its used and limit or its percent. */
export const meterUsedShare = (m: Meter): number | null => {
  if (m.unit === 'percent' && m.used !== null) return Math.min(1, Math.max(0, m.used / 100));
  if (m.limit !== null && m.limit > 0) {
    const used = m.used ?? (m.remaining !== null ? m.limit - m.remaining : null);
    if (used !== null) return Math.min(1, Math.max(0, used / m.limit));
  }
  return null;
};

/** A meter's name with its scope, such as "seven_day (opus)". */
export const meterName = (m: Meter): string => (m.scope ? `${m.meter} (${m.scope})` : m.meter);

export const formatDateTime = (iso: string | undefined): string => {
  if (!iso) return '—';
  const d = new Date(iso);
  return Number.isNaN(d.getTime())
    ? '—'
    : d.toLocaleString('en', { dateStyle: 'medium', timeStyle: 'short' });
};

/** How long ago, or until, an instant is, in the largest whole unit. */
export const formatRelative = (iso: string | undefined, now = Date.now()): string => {
  if (!iso) return '—';
  const ms = new Date(iso).getTime() - now;
  if (Number.isNaN(ms)) return '—';
  const units: Array<[Intl.RelativeTimeFormatUnit, number]> = [
    ['day', 86_400_000],
    ['hour', 3_600_000],
    ['minute', 60_000],
  ];
  const rtf = new Intl.RelativeTimeFormat('en', { numeric: 'auto' });
  for (const [unit, size] of units) {
    if (Math.abs(ms) >= size) return rtf.format(Math.round(ms / size), unit);
  }
  return rtf.format(Math.round(ms / 1000), 'second');
};

export const formatMS = (ms: number | undefined | null): string =>
  ms === undefined || ms === null
    ? '—'
    : ms >= 1000
      ? `${plain.format(ms / 1000)} s`
      : `${Math.round(ms)} ms`;

/** Today and the day n-1 days before it, as UTC YYYY-MM-DD. */
export const lastDays = (n: number, now = new Date()): { from: string; to: string } => {
  const to = now.toISOString().slice(0, 10);
  const from = new Date(now.getTime() - (n - 1) * 86_400_000).toISOString().slice(0, 10);
  return { from, to };
};
