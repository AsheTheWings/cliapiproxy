-- One row per stored generation: a completed request with its client,
-- credential, models, tokens, usage, and timing. No process deletes a row.
CREATE TABLE generations (
	generation_id uuid PRIMARY KEY,
	time timestamptz NOT NULL,
	trace_id text CHECK (trace_id ~ '^[0-9a-f]{32}$'),
	conversation_id text,
	status text NOT NULL CHECK (status IN ('completed', 'incomplete')),
	client_key_label text,
	client_key_fingerprint text CHECK (client_key_fingerprint ~ '^[0-9a-f]{12}$'),
	client_user_agent text,
	provider text NOT NULL,
	provider_group text,
	account_label text,
	account_scope text,
	credential_id text,
	key_fingerprint text CHECK (key_fingerprint ~ '^[0-9a-f]{12}$'),
	auth_type text,
	executor text,
	requested_model text,
	upstream_model text,
	response_model text,
	reasoning_effort text,
	requested_service_tier text,
	effective_service_tier text,
	response_service_tier text,
	stream boolean NOT NULL,
	input_uncached bigint NOT NULL CHECK (input_uncached >= 0),
	input_cache_read bigint NOT NULL CHECK (input_cache_read >= 0),
	input_cache_write bigint NOT NULL CHECK (input_cache_write >= 0),
	output_non_reasoning bigint NOT NULL CHECK (output_non_reasoning >= 0),
	output_reasoning bigint NOT NULL CHECK (output_reasoning >= 0),
	unclassified bigint NOT NULL CHECK (unclassified >= 0),
	total bigint NOT NULL CHECK (total >= 0),
	token_quality text NOT NULL,
	token_source text NOT NULL CHECK (token_source IN ('reported', 'estimated')),
	usage_raw jsonb,
	usage_amount numeric,
	usage_unit text CHECK (usage_unit IN ('credits', 'usd')),
	duration_ms bigint CHECK (duration_ms >= 0),
	first_token_ms bigint CHECK (first_token_ms >= 0),
	system_instruction_hash text REFERENCES system_instructions (hash),
	prior_snapshot_id bigint REFERENCES credential_usage_snapshots (snapshot_id),
	response_snapshot_id bigint REFERENCES credential_usage_snapshots (snapshot_id),
	metadata jsonb NOT NULL DEFAULT '{}',
	CHECK ((usage_amount IS NULL) = (usage_unit IS NULL))
);

CREATE INDEX generations_time ON generations (time);
CREATE INDEX generations_credential_time ON generations (credential_id, time);
CREATE INDEX generations_upstream_model_time ON generations (upstream_model, time);
CREATE INDEX generations_conversation_time ON generations (conversation_id, time);
CREATE INDEX generations_client_time ON generations (client_key_fingerprint, time);
CREATE INDEX generations_system_instruction_time ON generations (system_instruction_hash, time);
