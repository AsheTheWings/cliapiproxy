import { memo, useEffect, useMemo, useRef, type CSSProperties } from 'react';
import { useTranslation } from 'react-i18next';
import {
  API_KEY_STRENGTH_SEGMENTS,
  evaluateApiKeyStrength,
  type ApiKeyStrengthTier,
} from '@/utils/apiKeyStrength';
import { segmentFillDelayMs } from './clientKeyForm';
import styles from './ClientKeysPage.module.scss';

const TIER_COLORS: Record<ApiKeyStrengthTier, string> = {
  weak: 'var(--error-color)',
  fair: 'var(--amber-color)',
  good: 'var(--success-color)',
  strong: 'var(--success-color)',
};

const SEGMENT_INDEXES = Array.from({ length: API_KEY_STRENGTH_SEGMENTS }, (_, index) => index);

/** A guide to a key's strength: four segments light in turn; it never blocks saving. */
export const StrengthMeter = memo(function StrengthMeter({ value }: { value: string }) {
  const { t } = useTranslation();
  const { tier, segments } = useMemo(() => evaluateApiKeyStrength(value), [value]);

  // The previous segment count decides which segments wait their turn.
  const previousSegments = useRef(segments);
  const cascadeFrom = previousSegments.current;
  useEffect(() => {
    previousSegments.current = segments;
  }, [segments]);

  const empty = segments === 0;
  const label = empty ? t('client_keys.strength_empty') : t(`client_keys.strength_${tier}`);

  return (
    <div
      className={styles.strength}
      style={
        {
          '--strength-color': empty ? 'var(--text-quaternary)' : TIER_COLORS[tier],
        } as CSSProperties
      }
    >
      <div
        className={styles.strengthTrack}
        role="progressbar"
        aria-valuemin={0}
        aria-valuemax={API_KEY_STRENGTH_SEGMENTS}
        aria-valuenow={segments}
        aria-valuetext={label}
        aria-label={t('client_keys.strength')}
      >
        {SEGMENT_INDEXES.map((index) => (
          <span key={index} className={styles.strengthSegment}>
            <span
              className={styles.strengthSegmentFill}
              data-filled={index < segments}
              style={
                {
                  '--segment-delay': `${segmentFillDelayMs(index, segments, cascadeFrom)}ms`,
                } as CSSProperties
              }
            />
          </span>
        ))}
      </div>
      <span className={styles.strengthLabel} aria-hidden="true">
        {empty ? '—' : label}
      </span>
    </div>
  );
});
