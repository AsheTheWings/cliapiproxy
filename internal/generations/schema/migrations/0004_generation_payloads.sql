-- The client input, upstream input, and upstream response of each
-- generation, zstd-compressed. A pruned input is a residual that names the
-- later payload of its conversation and kind it is rebuilt from.
CREATE TABLE generation_payloads (
	payload_id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
	generation_id uuid NOT NULL REFERENCES generations (generation_id),
	kind text NOT NULL CHECK (kind IN ('client_input', 'upstream_input', 'upstream_response')),
	created_at timestamptz NOT NULL,
	conversation_id text,
	body bytea NOT NULL,
	content_type text NOT NULL,
	original_content_type text NOT NULL,
	compression text NOT NULL CHECK (compression = 'zstd'),
	body_sha256 text NOT NULL CHECK (body_sha256 ~ '^[0-9a-f]{64}$'),
	original_bytes bigint NOT NULL CHECK (original_bytes >= 0),
	stored_bytes bigint NOT NULL CHECK (stored_bytes >= 0),
	truncated boolean NOT NULL,
	base_payload_id bigint,
	rest_source_payload_id bigint,
	prefix_evaluated_at timestamptz,
	prefix_evaluation_version integer,
	prefix_failures integer NOT NULL DEFAULT 0 CHECK (prefix_failures >= 0),
	UNIQUE (generation_id, kind),
	UNIQUE (payload_id, conversation_id, kind),
	FOREIGN KEY (base_payload_id, conversation_id, kind)
		REFERENCES generation_payloads (payload_id, conversation_id, kind),
	FOREIGN KEY (rest_source_payload_id, conversation_id, kind)
		REFERENCES generation_payloads (payload_id, conversation_id, kind),
	CHECK (base_payload_id IS NULL OR (conversation_id IS NOT NULL AND kind IN ('client_input', 'upstream_input'))),
	CHECK (rest_source_payload_id IS NULL OR base_payload_id IS NOT NULL)
);

CREATE INDEX generation_payloads_conversation_kind_created
	ON generation_payloads (conversation_id, kind, created_at);
CREATE INDEX generation_payloads_base ON generation_payloads (base_payload_id);
CREATE INDEX generation_payloads_rest_source ON generation_payloads (rest_source_payload_id);
CREATE INDEX generation_payloads_unevaluated
	ON generation_payloads (conversation_id, kind, created_at)
	WHERE prefix_evaluated_at IS NULL
		AND base_payload_id IS NULL
		AND kind IN ('client_input', 'upstream_input');
