-- System instructions, stored once per SHA-256 of their text.
CREATE TABLE system_instructions (
	hash text PRIMARY KEY CHECK (hash ~ '^[0-9a-f]{64}$'),
	content text NOT NULL,
	first_seen_at timestamptz NOT NULL
);
