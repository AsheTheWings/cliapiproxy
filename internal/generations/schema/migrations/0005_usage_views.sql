-- Daily usage per provider, group, credential, account label, model, effort,
-- client fingerprint, and client label, refreshed concurrently by the server.
-- Absent values group as empty text, so the unique index covers every row.
CREATE MATERIALIZED VIEW generation_usage_daily AS
SELECT
	(time AT TIME ZONE 'UTC')::date AS day,
	provider,
	coalesce(provider_group, '') AS provider_group,
	coalesce(credential_id, '') AS credential_id,
	coalesce(account_label, '') AS account_label,
	coalesce(upstream_model, '') AS upstream_model,
	coalesce(reasoning_effort, '') AS reasoning_effort,
	coalesce(client_key_fingerprint, '') AS client_key_fingerprint,
	coalesce(client_key_label, '') AS client_key_label,
	count(*) AS generations,
	max(time) AS latest_time,
	sum(input_uncached) AS input_uncached,
	sum(input_cache_read) AS input_cache_read,
	sum(input_cache_write) AS input_cache_write,
	sum(output_non_reasoning) AS output_non_reasoning,
	sum(output_reasoning) AS output_reasoning,
	sum(unclassified) AS unclassified,
	sum(total) AS total,
	sum(usage_amount) FILTER (WHERE usage_unit = 'credits') AS usage_credits,
	sum(usage_amount) FILTER (WHERE usage_unit = 'usd') AS usage_usd
FROM generations
GROUP BY 1, 2, 3, 4, 5, 6, 7, 8, 9;

CREATE UNIQUE INDEX generation_usage_daily_key ON generation_usage_daily (
	day, provider, provider_group, credential_id, account_label,
	upstream_model, reasoning_effort, client_key_fingerprint, client_key_label
);

-- Each snapshot's meters with their change in their own unit since the
-- credential's previous snapshot of the same meter, and the generations the
-- credential served between the two, which are null for the first snapshot.
CREATE VIEW credential_usage_timeline AS
WITH readings AS (
	SELECT
		s.snapshot_id,
		s.credential_id,
		s.provider,
		s.provider_group,
		s.account_label,
		s.observed_at,
		s.source,
		s.plan,
		m.meter,
		m.scope,
		m.unit,
		m.used,
		m."limit",
		m.remaining,
		m.window_seconds,
		m.resets_at,
		lag(s.observed_at) OVER meter_history AS previous_observed_at,
		lag(m.used) OVER meter_history AS previous_used,
		lag(m.remaining) OVER meter_history AS previous_remaining
	FROM credential_usage_snapshots s
	JOIN credential_usage_meters m USING (snapshot_id)
	WINDOW meter_history AS (PARTITION BY s.credential_id, m.meter, m.scope ORDER BY s.observed_at, s.snapshot_id)
)
SELECT
	r.snapshot_id,
	r.credential_id,
	r.provider,
	r.provider_group,
	r.account_label,
	r.observed_at,
	r.source,
	r.plan,
	r.meter,
	r.scope,
	r.unit,
	r.used,
	r."limit",
	r.remaining,
	r.window_seconds,
	r.resets_at,
	r.previous_observed_at,
	r.used - r.previous_used AS used_change,
	r.remaining - r.previous_remaining AS remaining_change,
	g.generations,
	g.input_uncached,
	g.input_cache_read,
	g.input_cache_write,
	g.output_non_reasoning,
	g.output_reasoning,
	g.unclassified,
	g.total
FROM readings r
LEFT JOIN LATERAL (
	SELECT
		count(*) AS generations,
		coalesce(sum(input_uncached), 0) AS input_uncached,
		coalesce(sum(input_cache_read), 0) AS input_cache_read,
		coalesce(sum(input_cache_write), 0) AS input_cache_write,
		coalesce(sum(output_non_reasoning), 0) AS output_non_reasoning,
		coalesce(sum(output_reasoning), 0) AS output_reasoning,
		coalesce(sum(unclassified), 0) AS unclassified,
		coalesce(sum(total), 0) AS total
	FROM generations g
	WHERE g.credential_id = r.credential_id
		AND g.time > r.previous_observed_at
		AND g.time <= r.observed_at
) g ON r.previous_observed_at IS NOT NULL;
