# CLI Proxy API

CLI Proxy API is a proxy server that serves OpenAI (Chat Completions and
Responses), Gemini, Claude, and Codex compatible APIs backed by the accounts
of CLI tools: Codex, Claude Code, Grok Build, Antigravity, Kimi, Devin, and
Meta Muse Code. Clients and SDKs built for those APIs use the accounts through
it, with requests balanced across several accounts per provider.

This repository is a fork of
[router-for-me/CLIProxyAPI](https://github.com/router-for-me/CLIProxyAPI). It
builds the `CLIProxyAPI` server from `cmd/server` and publishes no release.

## Setup

[Building](docs/building.md) describes how to build the binary and the
container image.

The server reads its inputs from its environment, its command line, and two
kinds of YAML file; [configuration](docs/configuration.md) explains them. A
development run needs only the client API keys in its protected file:

```sh
mkdir -p "$XDG_RUNTIME_DIR/cliapiproxy"
install -m 600 /dev/null "$XDG_RUNTIME_DIR/cliapiproxy/protected.yaml"
printf 'access:\n  api-keys: [%s]\n' "$(openssl rand -hex 24)" \
  > "$XDG_RUNTIME_DIR/cliapiproxy/protected.yaml"
CLIPROXY_PROFILE=development ./CLIProxyAPI
```

## Usage

Clients send requests with one of the client API keys as a bearer token.
The OpenAI compatible API is under `/v1`, the Gemini API under `/v1beta`, and
the Codex backend API under `/backend-api/codex`.

Credentials are files in the auth directory. A login command runs the
provider's OAuth flow, writes the credential file, and exits; for example,
`-codex-device-login` signs in to Codex with a device code and `-xai-login`
to Grok Build. Adding, replacing, or removing a credential file takes effect
without a restart.

The Go packages under `sdk/` embed the proxy in other programs:
[usage](docs/sdk-usage.md), [executors and translators](docs/sdk-advanced.md),
[access](docs/sdk-access.md), and [watcher](docs/sdk-watcher.md).

## Development

- [Building](docs/building.md)
- [Configuration](docs/configuration.md)
- [Testing](docs/testing.md)

## License

This project is licensed under the MIT License; see [LICENSE](LICENSE).
