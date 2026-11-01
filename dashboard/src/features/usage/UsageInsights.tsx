import { useEffect, useMemo, useState, type ReactNode } from 'react';
import { useTranslation } from 'react-i18next';
import { LoadingSpinner } from '@/components/ui/LoadingSpinner';
import { Select } from '@/components/ui/Select';
import { storeApi, type Bucket, type LatencyPoint } from '@/services/api/store';
import { formatCompactNumber } from '@/utils/format';
import { formatMS, lastDays } from './format';
import {
  TOKEN_COLUMNS,
  cacheShare,
  dailyTokenRows,
  groupBuckets,
  intensity,
  reasoningShare,
} from './insights';
import { DailyStackChart, LinesChart, ShareBarChart } from './UsageCharts';
import { errorText, isStoreUnavailable } from './storeErrors';
import { StoreUnavailable } from './StoreUnavailable';
import styles from './UsagePages.module.scss';

const RANGES = [7, 30, 90];

type Dimension = 'model' | 'effort' | 'client';

const dimensionValue: Record<Dimension, (b: Bucket) => string> = {
  model: (b) => b.upstream_model ?? '',
  effort: (b) => b.reasoning_effort ?? '',
  client: (b) => b.client_key_fingerprint ?? '',
};

interface InsightData {
  byModel: Bucket[];
  byEffort: Bucket[];
  byClient: Bucket[];
  byCredentialModel: Bucket[];
  byCredentialClient: Bucket[];
  latency: LatencyPoint[];
}

const credentialName = (b: Bucket): string =>
  b.account_label || b.provider_group || b.credential_id || '—';

/** Client labels by fingerprint, falling back to the fingerprint. */
const clientNames = (buckets: Bucket[]): Map<string, string> => {
  const out = new Map<string, string>();
  for (const b of buckets) {
    const fingerprint = b.client_key_fingerprint ?? '';
    out.set(fingerprint, b.client_key_label || fingerprint || '—');
  }
  return out;
};

function ChartCard({
  title,
  children,
  extra,
}: {
  title: string;
  children: ReactNode;
  extra?: ReactNode;
}) {
  return (
    <section className={styles.chartBlock}>
      <div className={styles.chartHeader}>
        <h2 className={styles.chartTitle}>{title}</h2>
        {extra}
      </div>
      {children}
    </section>
  );
}

/** Usage over a day range: tokens, cache and reasoning shares, latency, and who uses what. */
export function UsageInsights() {
  const { t } = useTranslation();
  const [days, setDays] = useState(30);
  const [data, setData] = useState<InsightData | null>(null);
  const [error, setError] = useState<unknown>(null);
  const [dimension, setDimension] = useState<Dimension>('model');
  const [value, setValue] = useState('');
  const [cacheBy, setCacheBy] = useState<'model' | 'client'>('model');
  const [latencyModel, setLatencyModel] = useState('');

  useEffect(() => {
    let current = true;
    const { from, to } = lastDays(days);
    setData(null);
    setError(null);
    Promise.all([
      storeApi.summary(from, to, ['model']),
      storeApi.summary(from, to, ['effort']),
      storeApi.summary(from, to, ['client']),
      storeApi.summary(from, to, ['credential', 'model']),
      storeApi.summary(from, to, ['credential', 'client']),
      storeApi.latency(from, to),
    ])
      .then(([byModel, byEffort, byClient, byCredentialModel, byCredentialClient, latency]) => {
        if (current)
          setData({ byModel, byEffort, byClient, byCredentialModel, byCredentialClient, latency });
      })
      .catch((e) => {
        if (current) setError(e);
      });
    return () => {
      current = false;
    };
  }, [days]);

  const names = useMemo(() => clientNames(data?.byClient ?? []), [data]);
  const label = (dim: Dimension, v: string) =>
    dim === 'client' ? (names.get(v) ?? v) : v || t('usage.unknown');

  // The token chart's dimension values, by volume. The summaries are small, so
  // these derive on each render.
  const sourceFor = (dim: Dimension): Bucket[] =>
    (dim === 'model' ? data?.byModel : dim === 'effort' ? data?.byEffort : data?.byClient) ?? [];
  const values = [...groupBuckets(sourceFor(dimension), dimensionValue[dimension]).entries()]
    .sort(([, a], [, b]) => b.tokens.total - a.tokens.total)
    .map(([v]) => v);
  const selected = values.includes(value) ? value : (values[0] ?? '');
  const tokenRows = dailyTokenRows(
    sourceFor(dimension).filter((b) => dimensionValue[dimension](b) === selected)
  );

  const cacheRows = [...groupBuckets(sourceFor(cacheBy), dimensionValue[cacheBy]).entries()]
    .map(([v, e]) => ({
      name: label(cacheBy, v),
      share: cacheShare(e.tokens),
      total: e.tokens.total,
    }))
    .sort((a, b) => b.total - a.total);

  const reasoningRows = [...groupBuckets(sourceFor('effort'), dimensionValue.effort).entries()]
    .map(([v, e]) => ({ name: label('effort', v), share: reasoningShare(e.tokens) }))
    .sort((a, b) => a.name.localeCompare(b.name));

  const latencyModels = useMemo(() => {
    const counts = new Map<string, number>();
    for (const l of data?.latency ?? [])
      counts.set(l.upstream_model, (counts.get(l.upstream_model) ?? 0) + l.generations);
    return [...counts.entries()].sort(([, a], [, b]) => b - a).map(([m]) => m);
  }, [data]);
  const latencyFor = latencyModels.includes(latencyModel) ? latencyModel : (latencyModels[0] ?? '');
  const latencyRows = useMemo(
    () =>
      (data?.latency ?? [])
        .filter((l) => l.upstream_model === latencyFor)
        .map((l) => ({ day: l.day, p50: l.p50_ms, p90: l.p90_ms, p99: l.p99_ms })),
    [data, latencyFor]
  );

  // Per credential and model over the range.
  const credentialRows = useMemo(() => {
    const rows = new Map<
      string,
      {
        key: string;
        credentialId: string;
        credential: string;
        provider: string;
        model: string;
        generations: number;
        input: number;
        output: number;
        total: number;
        credits: number;
        usd: number;
      }
    >();
    for (const b of data?.byCredentialModel ?? []) {
      const k = `${b.credential_id}|${b.upstream_model}`;
      const r = rows.get(k) ?? {
        key: k,
        credentialId: b.credential_id ?? '',
        credential: credentialName(b),
        provider: b.provider ?? '',
        model: b.upstream_model || t('usage.unknown'),
        generations: 0,
        input: 0,
        output: 0,
        total: 0,
        credits: 0,
        usd: 0,
      };
      r.generations += b.generations;
      r.input += b.tokens.input_uncached + b.tokens.input_cache_read + b.tokens.input_cache_write;
      r.output += b.tokens.output_non_reasoning + b.tokens.output_reasoning;
      r.total += b.tokens.total;
      r.credits += b.usage_credits ?? 0;
      r.usd += b.usage_usd ?? 0;
      rows.set(k, r);
    }
    return [...rows.values()].sort(
      (a, b) => a.credential.localeCompare(b.credential) || b.total - a.total
    );
  }, [data, t]);

  // Clients against accounts.
  const matrix = useMemo(() => {
    const cells = new Map<string, { generations: number; total: number }>();
    const accounts = new Map<string, string>();
    const clients = new Set<string>();
    for (const b of data?.byCredentialClient ?? []) {
      const account = b.credential_id ?? '';
      const client = b.client_key_fingerprint ?? '';
      accounts.set(account, credentialName(b));
      clients.add(client);
      const k = `${client}|${account}`;
      const c = cells.get(k) ?? { generations: 0, total: 0 };
      c.generations += b.generations;
      c.total += b.tokens.total;
      cells.set(k, c);
    }
    const max = Math.max(0, ...[...cells.values()].map((c) => c.total));
    return { cells, accounts: [...accounts.entries()], clients: [...clients], max };
  }, [data]);

  if (isStoreUnavailable(error)) return <StoreUnavailable />;

  const rangeSelect = (
    <Select
      size="sm"
      fullWidth={false}
      value={String(days)}
      onChange={(v) => setDays(Number(v))}
      ariaLabel={t('usage.range')}
      options={RANGES.map((n) => ({ value: String(n), label: t('usage.last_days', { count: n }) }))}
    />
  );

  return (
    <div className={styles.insights}>
      <div className={styles.toolbar}>
        <span className={styles.muted}>{t('usage.summary_lag')}</span>
        {rangeSelect}
      </div>
      {error ? (
        <p className={styles.error}>{errorText(error)}</p>
      ) : !data ? (
        <LoadingSpinner />
      ) : (
        <>
          <ChartCard
            title={t('usage.tokens_title')}
            extra={
              <div className={styles.inline}>
                <Select
                  size="sm"
                  fullWidth={false}
                  value={dimension}
                  onChange={(v) => setDimension(v as Dimension)}
                  ariaLabel={t('usage.group_by')}
                  options={(['model', 'effort', 'client'] as Dimension[]).map((d) => ({
                    value: d,
                    label: t(`usage.dimension_${d}`),
                  }))}
                />
                <Select
                  size="sm"
                  fullWidth={false}
                  value={selected}
                  onChange={setValue}
                  ariaLabel={t(`usage.dimension_${dimension}`)}
                  options={values.map((v) => ({ value: v, label: label(dimension, v) }))}
                />
              </div>
            }
          >
            {tokenRows.length === 0 ? (
              <p className={styles.muted}>{t('usage.no_generations')}</p>
            ) : (
              <DailyStackChart
                rows={tokenRows}
                series={TOKEN_COLUMNS.map((c) => ({ key: c, label: t(`usage.column_${c}`) }))}
              />
            )}
          </ChartCard>

          <div className={styles.chartGrid}>
            <ChartCard
              title={t('usage.cache_title')}
              extra={
                <Select
                  size="sm"
                  fullWidth={false}
                  value={cacheBy}
                  onChange={(v) => setCacheBy(v as 'model' | 'client')}
                  ariaLabel={t('usage.group_by')}
                  options={[
                    { value: 'model', label: t('usage.dimension_model') },
                    { value: 'client', label: t('usage.dimension_client') },
                  ]}
                />
              }
            >
              <ShareBarChart rows={cacheRows} label={t('usage.cache_share')} />
            </ChartCard>
            <ChartCard title={t('usage.reasoning_title')}>
              <ShareBarChart rows={reasoningRows} label={t('usage.reasoning_share')} />
            </ChartCard>
          </div>

          <ChartCard
            title={t('usage.latency_title')}
            extra={
              <Select
                size="sm"
                fullWidth={false}
                value={latencyFor}
                onChange={setLatencyModel}
                ariaLabel={t('usage.dimension_model')}
                options={latencyModels.map((m) => ({ value: m, label: m || t('usage.unknown') }))}
              />
            }
          >
            {latencyRows.length === 0 ? (
              <p className={styles.muted}>{t('usage.no_latency')}</p>
            ) : (
              <LinesChart
                rows={latencyRows}
                xKey="day"
                format={formatMS}
                series={[
                  { key: 'p50', label: 'p50' },
                  { key: 'p90', label: 'p90' },
                  { key: 'p99', label: 'p99' },
                ]}
              />
            )}
          </ChartCard>

          <ChartCard title={t('usage.matrix_title')}>
            {matrix.clients.length === 0 ? (
              <p className={styles.muted}>{t('usage.no_generations')}</p>
            ) : (
              <div className={styles.scroll}>
                <table className={styles.matrix}>
                  <thead>
                    <tr>
                      <th>{t('usage.client_account')}</th>
                      {matrix.accounts.map(([id, name]) => (
                        <th key={id} title={id}>
                          {name}
                        </th>
                      ))}
                    </tr>
                  </thead>
                  <tbody>
                    {matrix.clients.map((client) => (
                      <tr key={client}>
                        <th>{names.get(client) ?? (client || '—')}</th>
                        {matrix.accounts.map(([account]) => {
                          const cell = matrix.cells.get(`${client}|${account}`);
                          const shade = intensity(cell?.total ?? 0, matrix.max);
                          return (
                            <td
                              key={account}
                              style={{
                                background: `color-mix(in srgb, #4f7cff ${shade > 0 ? Math.round(8 + shade * 57) : 0}%, transparent)`,
                              }}
                            >
                              {cell ? (
                                <>
                                  <span className={styles.cellMain}>
                                    {formatCompactNumber(cell.total)}
                                  </span>
                                  <span className={styles.cellSub}>
                                    {t('usage.generations_count', { count: cell.generations })}
                                  </span>
                                </>
                              ) : (
                                '—'
                              )}
                            </td>
                          );
                        })}
                      </tr>
                    ))}
                  </tbody>
                </table>
              </div>
            )}
          </ChartCard>

          <ChartCard title={t('usage.credentials_title')}>
            {credentialRows.length === 0 ? (
              <p className={styles.muted}>{t('usage.no_generations')}</p>
            ) : (
              <div className={styles.scroll}>
                <table className={styles.meters}>
                  <thead>
                    <tr>
                      <th>{t('generations.account')}</th>
                      <th>{t('generations.provider')}</th>
                      <th>{t('generations.upstream_model')}</th>
                      <th className={styles.num}>{t('usage.generations')}</th>
                      <th className={styles.num}>{t('generations.input')}</th>
                      <th className={styles.num}>{t('generations.output')}</th>
                      <th className={styles.num}>{t('usage.total')}</th>
                      <th className={styles.num}>{t('usage.credits')}</th>
                      <th className={styles.num}>{t('usage.usd')}</th>
                    </tr>
                  </thead>
                  <tbody>
                    {credentialRows.map((r) => (
                      <tr key={r.key}>
                        <td title={r.credentialId}>{r.credential}</td>
                        <td>{r.provider}</td>
                        <td>
                          <code>{r.model}</code>
                        </td>
                        <td className={styles.num}>{r.generations}</td>
                        <td className={styles.num}>{formatCompactNumber(r.input)}</td>
                        <td className={styles.num}>{formatCompactNumber(r.output)}</td>
                        <td className={styles.num}>{formatCompactNumber(r.total)}</td>
                        <td className={styles.num}>{r.credits ? r.credits.toFixed(2) : '—'}</td>
                        <td className={styles.num}>{r.usd ? `$${r.usd.toFixed(2)}` : '—'}</td>
                      </tr>
                    ))}
                  </tbody>
                </table>
              </div>
            )}
          </ChartCard>
        </>
      )}
    </div>
  );
}
