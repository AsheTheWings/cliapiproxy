/**
 * The usage charts, drawn with Recharts in the dashboard's theme colors.
 */
import {
  Bar,
  BarChart,
  CartesianGrid,
  ComposedChart,
  Legend,
  Line,
  LineChart,
  ResponsiveContainer,
  Tooltip,
  XAxis,
  YAxis,
} from 'recharts';
import { formatCompactNumber, formatPercent } from '@/utils/format';
import type { TokenColumn } from './insights';

/** Series colors that read on the light and dark themes alike. */
const SERIES_COLORS = [
  '#4f7cff',
  '#e0884a',
  '#3fae7a',
  '#c95ec4',
  '#d4b13f',
  '#8a8f98',
  '#e05a5a',
  '#4fb6c9',
];

const axis = { stroke: 'var(--text-tertiary)', fontSize: 12 };
const legendStyle = { fontSize: 12 };
const tooltipStyle = {
  background: 'var(--floating-surface, var(--bg-primary))',
  border: '1px solid var(--border-color)',
  borderRadius: 8,
  color: 'var(--text-primary)',
};

export interface Series {
  key: string;
  label: string;
}

interface DailyStackProps {
  rows: ReadonlyArray<object>;
  series: ReadonlyArray<{ key: TokenColumn | string; label: string }>;
  height?: number;
}

/** Stacked daily bars, one stack per day. */
export function DailyStackChart({ rows, series, height = 260 }: DailyStackProps) {
  return (
    <ResponsiveContainer width="100%" height={height}>
      <BarChart data={rows} margin={{ top: 8, right: 8, bottom: 0, left: 0 }}>
        <CartesianGrid stroke="var(--border-color)" vertical={false} />
        <XAxis dataKey="day" tick={axis} stroke="var(--border-color)" />
        <YAxis
          tick={axis}
          stroke="var(--border-color)"
          tickFormatter={formatCompactNumber}
          width={56}
        />
        <Tooltip contentStyle={tooltipStyle} formatter={(v) => formatCompactNumber(Number(v))} />
        <Legend wrapperStyle={legendStyle} />
        {series.map((s, i) => (
          <Bar
            key={s.key}
            dataKey={s.key}
            name={s.label}
            stackId="tokens"
            fill={SERIES_COLORS[i % SERIES_COLORS.length]}
          />
        ))}
      </BarChart>
    </ResponsiveContainer>
  );
}

interface ShareBarsProps {
  rows: Array<{ name: string; share: number | null }>;
  label: string;
  height?: number;
}

/** Horizontal bars of shares from 0 to 1; a row without a share draws no bar. */
export function ShareBarChart({ rows, label, height }: ShareBarsProps) {
  return (
    <ResponsiveContainer width="100%" height={height ?? Math.max(120, rows.length * 34 + 40)}>
      <BarChart data={rows} layout="vertical" margin={{ top: 4, right: 16, bottom: 0, left: 8 }}>
        <CartesianGrid stroke="var(--border-color)" horizontal={false} />
        <XAxis
          type="number"
          domain={[0, 1]}
          tick={axis}
          tickFormatter={(v) => formatPercent(Number(v) * 100, 0)}
        />
        <YAxis type="category" dataKey="name" tick={axis} width={150} />
        <Tooltip
          contentStyle={tooltipStyle}
          formatter={(v) => (v === null || v === undefined ? '—' : formatPercent(Number(v) * 100))}
        />
        <Bar dataKey="share" name={label} fill={SERIES_COLORS[0]} />
      </BarChart>
    </ResponsiveContainer>
  );
}

interface LinesProps {
  rows: Array<Record<string, number | string | null>>;
  xKey: string;
  series: Series[];
  format?: (value: number) => string;
  /** A series drawn as bars on a second axis, such as generation counts. */
  bars?: Series;
  /** Places rows by their xKey, in epoch milliseconds, across this domain. */
  time?: {
    domain: [number, number];
    tick: (ms: number) => string;
    label: (ms: number) => string;
  };
  height?: number;
}

/** Lines over time, optionally with bars on a right-hand axis. */
export function LinesChart({
  rows,
  xKey,
  series,
  format = formatCompactNumber,
  bars,
  time,
  height = 260,
}: LinesProps) {
  const lines = series.map((s, i) => (
    <Line
      key={s.key}
      type={time ? 'linear' : 'monotone'}
      dataKey={s.key}
      name={s.label}
      stroke={SERIES_COLORS[i % SERIES_COLORS.length]}
      dot={time ? { r: 2 } : false}
      strokeWidth={2}
      connectNulls
      yAxisId="left"
    />
  ));
  const common = (
    <>
      <CartesianGrid stroke="var(--border-color)" vertical={false} />
      {time ? (
        <XAxis
          dataKey={xKey}
          type="number"
          scale="time"
          domain={time.domain}
          tickFormatter={(v) => time.tick(Number(v))}
          tick={axis}
          stroke="var(--border-color)"
          minTickGap={24}
        />
      ) : (
        <XAxis dataKey={xKey} tick={axis} stroke="var(--border-color)" minTickGap={24} />
      )}
      <YAxis
        yAxisId="left"
        tick={axis}
        stroke="var(--border-color)"
        tickFormatter={(v) => format(Number(v))}
        width={60}
      />
      <Tooltip
        contentStyle={tooltipStyle}
        labelFormatter={time ? (v) => time.label(Number(v)) : undefined}
        formatter={(v) => (v === null || v === undefined ? '—' : format(Number(v)))}
      />
      <Legend wrapperStyle={legendStyle} />
    </>
  );
  return (
    <ResponsiveContainer width="100%" height={height}>
      {bars ? (
        <ComposedChart data={rows} margin={{ top: 8, right: 8, bottom: 0, left: 0 }}>
          {common}
          <YAxis
            yAxisId="right"
            orientation="right"
            tick={axis}
            stroke="var(--border-color)"
            width={40}
            allowDecimals={false}
          />
          <Bar
            yAxisId="right"
            dataKey={bars.key}
            name={bars.label}
            fill="var(--border-hover, #8a8f98)"
            opacity={0.5}
            barSize={time ? 6 : undefined}
          />
          {lines}
        </ComposedChart>
      ) : (
        <LineChart data={rows} margin={{ top: 8, right: 8, bottom: 0, left: 0 }}>
          {common}
          {lines}
        </LineChart>
      )}
    </ResponsiveContainer>
  );
}
