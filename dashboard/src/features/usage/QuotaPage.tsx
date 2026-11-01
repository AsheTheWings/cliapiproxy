import { useCallback, useEffect, useMemo, useState } from 'react';
import { useTranslation } from 'react-i18next';
import { Button } from '@/components/ui/Button';
import { EmptyState } from '@/components/ui/EmptyState';
import { LoadingSpinner } from '@/components/ui/LoadingSpinner';
import { Select } from '@/components/ui/Select';
import { IconRefreshCw } from '@/components/ui/icons';
import { useHeaderRefresh } from '@/hooks/useHeaderRefresh';
import { storeApi, type CredentialUsage, type TimelinePoint } from '@/services/api/store';
import { useNotificationStore } from '@/stores';
import {
  formatDateTime,
  formatMeterValue,
  formatRelative,
  meterName,
  meterUsedShare,
} from './format';
import { LinesChart } from './UsageCharts';
import { errorText, isStoreUnavailable } from './storeErrors';
import { StoreUnavailable } from './StoreUnavailable';
import styles from './UsagePages.module.scss';

const TIMELINE_RANGES = [
  { value: '1', days: 1 },
  { value: '7', days: 7 },
  { value: '30', days: 30 },
];

const credentialName = (c: CredentialUsage): string =>
  c.account_label || c.provider_group || c.credential_id;

interface BurnChartProps {
  credential: CredentialUsage;
}

/** Each meter's used value over time, with the generations between snapshots. */
function BurnChart({ credential }: BurnChartProps) {
  const { t } = useTranslation();
  const [range, setRange] = useState('7');
  const [points, setPoints] = useState<TimelinePoint[] | null>(null);
  const [error, setError] = useState('');
  const [domain, setDomain] = useState<[number, number]>([0, 0]);

  useEffect(() => {
    let current = true;
    const days = TIMELINE_RANGES.find((r) => r.value === range)?.days ?? 7;
    const to = new Date();
    const from = new Date(to.getTime() - days * 86_400_000);
    setDomain([from.getTime(), to.getTime()]);
    setPoints(null);
    setError('');
    storeApi
      .timeline(credential.credential_id, from.toISOString(), to.toISOString())
      .then((p) => {
        if (current) setPoints(p);
      })
      .catch((e) => {
        if (current) setError(errorText(e));
      });
    return () => {
      current = false;
    };
  }, [credential.credential_id, range]);

  // One chart per unit and one row per snapshot, placed at its time; each meter is its used
  // value, or its remaining one when it reports no use.
  const charts = useMemo(() => {
    const byUnit = new Map<
      string,
      { rows: Map<string, Record<string, number | string | null>>; names: Map<string, string> }
    >();
    for (const p of points ?? []) {
      const unit = p.meter.unit;
      const chart = byUnit.get(unit) ?? { rows: new Map(), names: new Map() };
      byUnit.set(unit, chart);
      const key = `${p.meter.meter}|${p.meter.scope ?? ''}`;
      const useRemaining = p.meter.used === null;
      chart.names.set(
        key,
        `${meterName(p.meter)}${useRemaining ? ` ${t('usage.remaining')}` : ''}`
      );
      const row = chart.rows.get(p.observed_at) ?? {
        at: new Date(p.observed_at).getTime(),
        generations: 0,
      };
      row[key] = useRemaining ? p.meter.remaining : p.meter.used;
      row.generations = Math.max(Number(row.generations), p.generations ?? 0);
      chart.rows.set(p.observed_at, row);
    }
    return [...byUnit.entries()]
      .sort(([a], [b]) => a.localeCompare(b))
      .map(([unit, chart]) => ({
        unit,
        rows: [...chart.rows.values()].sort((a, b) => Number(a.at) - Number(b.at)),
        series: [...chart.names.entries()].map(([key, label]) => ({ key, label })),
      }));
  }, [points, t]);

  const time = useMemo(() => {
    const oneDay = domain[1] - domain[0] <= 86_400_000;
    return {
      domain,
      tick: (ms: number) =>
        new Date(ms).toLocaleString(
          'en',
          oneDay ? { hour: 'numeric', minute: '2-digit' } : { month: 'short', day: 'numeric' }
        ),
      label: (ms: number) => formatDateTime(new Date(ms).toISOString()),
    };
  }, [domain]);

  return (
    <div className={styles.chartBlock}>
      <div className={styles.chartHeader}>
        <span className={styles.chartTitle}>{t('usage.burn_title')}</span>
        <Select
          size="sm"
          fullWidth={false}
          value={range}
          onChange={setRange}
          ariaLabel={t('usage.range')}
          options={TIMELINE_RANGES.map((r) => ({
            value: r.value,
            label: t('usage.last_days', { count: r.days }),
          }))}
        />
      </div>
      {error ? (
        <p className={styles.error}>{error}</p>
      ) : points === null ? (
        <LoadingSpinner />
      ) : charts.length === 0 ? (
        <p className={styles.muted}>{t('usage.no_snapshots')}</p>
      ) : (
        charts.map((c) => (
          <LinesChart
            key={c.unit}
            rows={c.rows}
            xKey="at"
            series={c.series}
            time={time}
            format={(v) => formatMeterValue(v, c.unit)}
            bars={{ key: 'generations', label: t('usage.generations_between') }}
          />
        ))
      )}
    </div>
  );
}

interface CredentialCardProps {
  credential: CredentialUsage;
  refreshing: boolean;
  onRefresh: () => void;
}

function CredentialCard({ credential, refreshing, onRefresh }: CredentialCardProps) {
  const { t } = useTranslation();
  const [open, setOpen] = useState(false);
  const latest = credential.latest;
  return (
    <article className={styles.card}>
      <header className={styles.cardHeader}>
        <div className={styles.cardTitle}>
          <span className={styles.name}>{credentialName(credential)}</span>
          <span className={styles.muted}>
            {[credential.provider_group, latest?.plan].filter(Boolean).join(' · ')}
          </span>
        </div>
        <div className={styles.cardActions}>
          <span
            className={styles.muted}
            title={latest ? formatDateTime(latest.observed_at) : undefined}
          >
            {latest
              ? t('usage.observed', { when: formatRelative(latest.observed_at) })
              : t('usage.no_snapshot')}
          </span>
          {credential.configured && credential.usage_source && (
            <Button variant="secondary" size="sm" loading={refreshing} onClick={onRefresh}>
              <IconRefreshCw size={14} /> {t('common.refresh')}
            </Button>
          )}
        </div>
      </header>
      {!credential.configured && <p className={styles.muted}>{t('usage.not_configured')}</p>}
      {latest && latest.meters.length > 0 && (
        <table className={styles.meters}>
          <thead>
            <tr>
              <th>{t('usage.meter')}</th>
              <th className={styles.num}>{t('usage.used')}</th>
              <th className={styles.num}>{t('usage.limit')}</th>
              <th className={styles.num}>{t('usage.remaining_header')}</th>
              <th>{t('usage.resets')}</th>
            </tr>
          </thead>
          <tbody>
            {latest.meters.map((m) => {
              const usedShare = meterUsedShare(m);
              return (
                <tr key={`${m.meter}|${m.scope ?? ''}`}>
                  <td>
                    <span>{meterName(m)}</span>
                    {usedShare !== null && (
                      <span className={styles.bar} aria-hidden="true">
                        <span
                          className={usedShare > 0.9 ? styles.barHigh : styles.barFill}
                          style={{ width: `${usedShare * 100}%` }}
                        />
                      </span>
                    )}
                  </td>
                  <td className={styles.num}>{formatMeterValue(m.used, m.unit)}</td>
                  <td className={styles.num}>{formatMeterValue(m.limit, m.unit)}</td>
                  <td className={styles.num}>{formatMeterValue(m.remaining, m.unit)}</td>
                  <td title={m.resets_at ? formatDateTime(m.resets_at) : undefined}>
                    {m.resets_at ? formatRelative(m.resets_at) : '—'}
                  </td>
                </tr>
              );
            })}
          </tbody>
        </table>
      )}
      {latest && (
        <button
          type="button"
          className={styles.disclosure}
          onClick={() => setOpen((v) => !v)}
          aria-expanded={open}
        >
          {open ? t('usage.hide_burn') : t('usage.show_burn')}
        </button>
      )}
      {open && <BurnChart credential={credential} />}
    </article>
  );
}

/** Every credential's latest usage, by provider. */
export function QuotaPage() {
  const { t } = useTranslation();
  const { showNotification } = useNotificationStore();
  const [credentials, setCredentials] = useState<CredentialUsage[] | null>(null);
  const [error, setError] = useState<unknown>(null);
  const [refreshing, setRefreshing] = useState<string | null>(null);

  const load = useCallback(async () => {
    try {
      setCredentials(await storeApi.quota());
      setError(null);
    } catch (e) {
      setError(e);
    }
  }, []);
  useHeaderRefresh(load);
  useEffect(() => {
    void load();
  }, [load]);

  const refresh = async (c: CredentialUsage) => {
    setRefreshing(c.credential_id);
    try {
      const { outcome, credential } = await storeApi.refreshQuota(c.credential_id);
      setCredentials(
        (list) =>
          list?.map((x) => (x.credential_id === credential.credential_id ? credential : x)) ?? null
      );
      const failed = outcome !== 'ok' && outcome !== 'unchanged';
      showNotification(
        t(failed ? 'usage.refresh_failed' : 'usage.refreshed', {
          name: credentialName(c),
          outcome,
        }),
        failed ? 'error' : 'success'
      );
    } catch (e) {
      showNotification(
        t('usage.refresh_failed', { name: credentialName(c), outcome: errorText(e) }),
        'error'
      );
    } finally {
      setRefreshing(null);
    }
  };

  // Credentials with neither a usage source nor a stored snapshot have nothing to show.
  const providers = useMemo(() => {
    const groups = new Map<string, CredentialUsage[]>();
    for (const c of credentials ?? []) {
      if (!c.usage_source && !c.latest) continue;
      groups.set(c.provider, [...(groups.get(c.provider) ?? []), c]);
    }
    return [...groups.entries()].sort(([a], [b]) => a.localeCompare(b));
  }, [credentials]);

  return (
    <div className={styles.page}>
      <header className={styles.header}>
        <h1 className={styles.title}>{t('nav.quota')}</h1>
        <p className={styles.meta}>{t('usage.quota_subtitle')}</p>
      </header>
      {isStoreUnavailable(error) ? (
        <StoreUnavailable />
      ) : error ? (
        <p className={styles.error}>{errorText(error)}</p>
      ) : credentials === null ? (
        <LoadingSpinner />
      ) : providers.length === 0 ? (
        <EmptyState
          title={t('usage.quota_empty_title')}
          description={t('usage.quota_empty_desc')}
        />
      ) : (
        providers.map(([provider, list]) => (
          <section key={provider} className={styles.section}>
            <h2 className={styles.sectionTitle}>{provider}</h2>
            <div className={styles.cards}>
              {list.map((c) => (
                <CredentialCard
                  key={c.credential_id}
                  credential={c}
                  refreshing={refreshing === c.credential_id}
                  onRefresh={() => void refresh(c)}
                />
              ))}
            </div>
          </section>
        ))
      )}
    </div>
  );
}
