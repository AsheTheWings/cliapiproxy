-- Full inputs by the evaluation version that last pruned them, so a pass
-- with a newer evaluator finds the inputs it must evaluate again.
CREATE INDEX generation_payloads_evaluation_version
	ON generation_payloads (prefix_evaluation_version)
	WHERE base_payload_id IS NULL
		AND kind IN ('client_input', 'upstream_input');
