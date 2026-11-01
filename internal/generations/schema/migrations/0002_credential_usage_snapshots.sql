-- Each credential's plan, credits, and quota windows, one snapshot per
-- changed reading, with one meter row per reading in its own unit.
CREATE TABLE credential_usage_snapshots (
	snapshot_id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
	credential_id text NOT NULL,
	provider text NOT NULL,
	provider_group text,
	account_label text,
	account_scope text,
	key_fingerprint text CHECK (key_fingerprint ~ '^[0-9a-f]{12}$'),
	observed_at timestamptz NOT NULL,
	source text NOT NULL CHECK (source IN ('poll', 'response_headers')),
	plan text,
	readings_sha256 text NOT NULL CHECK (readings_sha256 ~ '^[0-9a-f]{64}$'),
	raw jsonb
);

CREATE INDEX credential_usage_snapshots_credential_observed
	ON credential_usage_snapshots (credential_id, observed_at);

CREATE TABLE credential_usage_meters (
	snapshot_id bigint NOT NULL REFERENCES credential_usage_snapshots (snapshot_id),
	meter text NOT NULL,
	scope text NOT NULL DEFAULT '',
	unit text NOT NULL CHECK (unit IN ('credits', 'percent', 'usd', 'requests')),
	used numeric,
	"limit" numeric,
	remaining numeric,
	window_seconds bigint CHECK (window_seconds > 0),
	resets_at timestamptz,
	PRIMARY KEY (snapshot_id, meter, scope)
);
