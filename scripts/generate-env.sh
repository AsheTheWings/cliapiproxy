#!/bin/sh
# Regenerate the tracked .env from internal/inputs/schema.json.
set -eu
cd "$(dirname "$0")/.."
go run ./internal/inputs/dotenvgen > .env.tmp
mv .env.tmp .env
