-- Instructions retain every byte, including NUL and invalid UTF-8.
ALTER TABLE system_instructions
	ALTER COLUMN content TYPE bytea USING convert_to(content, 'UTF8');
ALTER TABLE system_instructions
	ADD CONSTRAINT system_instruction_content_hash
	CHECK (encode(sha256(content), 'hex') = hash);
