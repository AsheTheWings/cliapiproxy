import { useEffect, useState } from 'react';
import { useTranslation } from 'react-i18next';
import { Button } from '@/components/ui/Button';
import { LoadingSpinner } from '@/components/ui/LoadingSpinner';
import { Sheet } from '@/components/ui/Sheet';
import { IconExternalLink } from '@/components/ui/icons';
import {
  storeApi,
  type GenerationDetail,
  type Payload,
  type PayloadKind,
  type UsageSnapshot,
} from '@/services/api/store';
import { formatFileSize } from '@/utils/format';
import { formatDateTime, formatMS, formatMeterValue, meterName } from './format';
import { prettyEventData, viewPayload } from './insights';
import { errorText } from './storeErrors';
import styles from './UsagePages.module.scss';

const KINDS: PayloadKind[] = ['client_input', 'upstream_input', 'upstream_response'];

function SnapshotFields({ title, snapshot }: { title: string; snapshot: UsageSnapshot }) {
  const { t } = useTranslation();
  return (
    <section className={styles.snapshot}>
      <h3 className={styles.chartTitle}>{title}</h3>
      <p className={styles.muted}>
        {[
          formatDateTime(snapshot.observed_at),
          t(`generations.snapshot_source_${snapshot.source}`, { defaultValue: snapshot.source }),
          snapshot.plan,
          `#${snapshot.snapshot_id}`,
        ]
          .filter(Boolean)
          .join(' · ')}
      </p>
      <ul className={styles.snapshotMeters}>
        {snapshot.meters.map((m) => (
          <li key={`${m.meter}|${m.scope ?? ''}`}>
            <span>{meterName(m)}</span>
            <span>
              {t('generations.snapshot_meter', {
                used: formatMeterValue(m.used, m.unit),
                limit: formatMeterValue(m.limit, m.unit),
                remaining: formatMeterValue(m.remaining, m.unit),
              })}
            </span>
          </li>
        ))}
      </ul>
    </section>
  );
}

function PayloadViewer({ payload }: { payload: Payload }) {
  const { t } = useTranslation();
  const view = viewPayload(payload.body, payload.contentType);
  return (
    <div className={styles.payload}>
      <div className={styles.payloadMeta}>
        <code>{payload.contentType || '—'}</code>
        {payload.rebuilt && <span className={styles.tag}>{t('generations.rebuilt')}</span>}
        {payload.truncated && <span className={styles.tagWarn}>{t('generations.truncated')}</span>}
      </div>
      {view.kind === 'events' ? (
        <ol className={styles.events}>
          {view.events.map((e, i) => (
            <li key={i}>
              {e.event && <span className={styles.eventName}>{e.event}</span>}
              <pre className={styles.code}>{prettyEventData(e.data)}</pre>
            </li>
          ))}
        </ol>
      ) : (
        <pre className={styles.code}>{view.text}</pre>
      )}
    </div>
  );
}

interface GenerationSheetProps {
  id: string | null;
  open: boolean;
  onClose: () => void;
  onConversation: (conversationId: string) => void;
}

/** One generation's columns, linked usage, trace, and payloads. */
export function GenerationSheet({ id, open, onClose, onConversation }: GenerationSheetProps) {
  const { t } = useTranslation();
  const [detail, setDetail] = useState<GenerationDetail | null>(null);
  const [error, setError] = useState('');
  const [kind, setKind] = useState<PayloadKind>('client_input');
  const [payload, setPayload] = useState<Payload | null>(null);
  const [payloadError, setPayloadError] = useState('');

  useEffect(() => {
    if (!id || !open) return;
    let current = true;
    setDetail(null);
    setError('');
    storeApi
      .generation(id)
      .then((d) => {
        if (current) setDetail(d);
      })
      .catch((e) => {
        if (current) setError(errorText(e));
      });
    return () => {
      current = false;
    };
  }, [id, open]);

  useEffect(() => {
    if (!id || !open) return;
    let current = true;
    setPayload(null);
    setPayloadError('');
    storeApi
      .payload(id, kind)
      .then((p) => {
        if (current) setPayload(p);
      })
      .catch((e) => {
        if (current) setPayloadError(errorText(e));
      });
    return () => {
      current = false;
    };
  }, [id, open, kind]);

  const tk = detail?.tokens;
  const rows: Array<[string, string | undefined]> = detail
    ? [
        [t('generations.time'), formatDateTime(detail.time)],
        [t('generations.status'), detail.status],
        [t('generations.client'), detail.client_key_label],
        [t('generations.client_fingerprint'), detail.client_key_fingerprint],
        [t('generations.user_agent'), detail.client_user_agent],
        [
          t('generations.provider'),
          [detail.provider, detail.provider_group].filter(Boolean).join(' · '),
        ],
        [t('generations.executor'), detail.executor],
        [t('generations.account'), detail.account_label],
        [t('generations.account_scope'), detail.account_scope],
        [t('generations.credential'), detail.credential_id],
        [t('generations.key_fingerprint'), detail.key_fingerprint],
        [t('generations.auth_type'), detail.auth_type],
        [t('generations.requested_model'), detail.requested_model],
        [t('generations.upstream_model'), detail.upstream_model],
        [t('generations.response_model'), detail.response_model],
        [t('generations.effort'), detail.reasoning_effort],
        [t('generations.requested_service_tier'), detail.requested_service_tier],
        [t('generations.effective_service_tier'), detail.effective_service_tier],
        [t('generations.response_service_tier'), detail.response_service_tier],
        [
          t('generations.tokens'),
          tk
            ? t('generations.token_breakdown', {
                uncached: tk.input_uncached,
                read: tk.input_cache_read,
                write: tk.input_cache_write,
                output: tk.output_non_reasoning,
                reasoning: tk.output_reasoning,
                unclassified: tk.unclassified,
                total: tk.total,
              })
            : undefined,
        ],
        [t('generations.token_source'), `${detail.token_source} · ${detail.token_quality}`],
        [
          t('generations.usage'),
          detail.usage_amount !== undefined
            ? `${detail.usage_amount} ${detail.usage_unit ?? ''}`
            : undefined,
        ],
        [t('generations.duration'), formatMS(detail.duration_ms)],
        [t('generations.first_token'), formatMS(detail.first_token_ms)],
        [t('generations.stream'), detail.stream ? t('common.yes') : t('common.no')],
        [t('generations.trace_id'), detail.trace_id],
        [t('generations.conversation_id'), detail.conversation_id],
        [t('generations.system_instruction'), detail.system_instruction_hash],
      ]
    : [];

  return (
    <Sheet
      open={open}
      onClose={onClose}
      size="xl"
      title={t('generations.detail_title')}
      description={id ?? undefined}
    >
      {error ? (
        <p className={styles.error}>{error}</p>
      ) : !detail ? (
        <LoadingSpinner />
      ) : (
        <div className={styles.detail}>
          <div className={styles.detailActions}>
            {detail.trace_url && (
              <a
                className={styles.link}
                href={detail.trace_url}
                target="_blank"
                rel="noreferrer noopener"
              >
                <IconExternalLink size={14} /> {t('generations.trace')}
              </a>
            )}
            {detail.conversation_id && (
              <Button
                variant="secondary"
                size="sm"
                onClick={() => onConversation(detail.conversation_id!)}
              >
                {t('generations.conversation')}
              </Button>
            )}
          </div>
          <dl className={styles.fields}>
            {rows
              .filter(([, value]) => value)
              .map(([label, value]) => (
                <div key={label} className={styles.field}>
                  <dt>{label}</dt>
                  <dd>{value}</dd>
                </div>
              ))}
          </dl>
          {detail.usage_raw !== undefined && detail.usage_raw !== null && (
            <section className={styles.snapshot}>
              <h3 className={styles.chartTitle}>{t('generations.usage_raw')}</h3>
              <pre className={styles.code}>{JSON.stringify(detail.usage_raw, null, 2)}</pre>
            </section>
          )}
          {detail.prior_snapshot && (
            <SnapshotFields
              title={t('generations.prior_snapshot')}
              snapshot={detail.prior_snapshot}
            />
          )}
          {detail.response_snapshot && (
            <SnapshotFields
              title={t('generations.response_snapshot')}
              snapshot={detail.response_snapshot}
            />
          )}
          <div className={styles.tabs} role="tablist">
            {KINDS.map((k) => {
              const info = detail.payloads.find((p) => p.kind === k);
              return (
                <button
                  key={k}
                  type="button"
                  role="tab"
                  aria-selected={kind === k}
                  className={kind === k ? styles.tabActive : styles.tab}
                  onClick={() => setKind(k)}
                  disabled={!info}
                >
                  {t(`generations.kind_${k}`)}
                  {info && (
                    <span className={styles.muted}> {formatFileSize(info.original_bytes)}</span>
                  )}
                </button>
              );
            })}
          </div>
          {payloadError ? (
            <p className={styles.error}>{payloadError}</p>
          ) : payload ? (
            <PayloadViewer payload={payload} />
          ) : (
            <LoadingSpinner />
          )}
        </div>
      )}
    </Sheet>
  );
}
