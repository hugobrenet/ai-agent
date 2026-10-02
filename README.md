# opensvc-ai-agent

Standalone AI agent for OpenSVC cluster diagnostics, usable with `om ai`.

## Requirements

- Go 1.25.5 or later to build.
- A configured OpenSVC MCP server accessible over HTTPS.
- An LLM endpoint and model.
- A TLS certificate covering the agent's hostname, with its private key.

## Install

From the repository root, on a Linux host with systemd:

```bash
go build -o bin/opensvc-ai-agentd ./cmd/opensvc-ai-agentd
sudo useradd --system --user-group --no-create-home --shell /usr/sbin/nologin opensvc-ai
sudo install -Dm755 bin/opensvc-ai-agentd /usr/local/libexec/opensvc-ai-agentd
sudo install -d -m750 -o opensvc-ai -g opensvc-ai /etc/opensvc-ai
sudo install -m644 deploy/systemd/opensvc-ai-agent.service /etc/systemd/system/
```

Place the TLS certificate at `/etc/opensvc-ai/agent.crt` and the private key
at `/etc/opensvc-ai/agent.key`. Both must be readable by `opensvc-ai`; restrict
the private key to that user.

## Configure

Create `/etc/opensvc-ai/agent-llm.env`, owned by root with mode `0600`:

```dotenv
OPENSVC_AI_LISTEN_ADDR=0.0.0.0:8090
OPENSVC_AI_MCP_URL=https://mcp.example.test/mcp

OPENSVC_AI_LLM_PROTOCOL=responses
OPENSVC_AI_LLM_BASE_URL=https://llm.example.test/v1
OPENSVC_AI_LLM_MODEL=your-model
OPENSVC_AI_LLM_AUTH_MODE=bearer
OPENSVC_AI_LLM_API_TOKEN=replace-me
```

Replace the example values. Use `chat_completions` instead of `responses` when
required by the provider. For a provider without authentication, set
`OPENSVC_AI_LLM_AUTH_MODE=none` and omit the API token.

Optional: `OPENSVC_AI_MCP_CA_FILE` supplies a private CA bundle for MCP HTTPS.
Otherwise, system CA roots are used. Restrict network access to the agent port.

The service creates its state directory at `/var/lib/opensvc-ai-agent`.

## Start

```bash
sudo systemctl daemon-reload
sudo systemctl enable --now opensvc-ai-agent
sudo systemctl status opensvc-ai-agent
```

Check health using the hostname covered by the certificate:

```bash
curl https://agent.example.test:8090/health
```

Add `--cacert /path/to/ca.pem` if the agent uses a private CA.

## Use with `om ai`

On an OpenSVC node:

```bash
export OPENSVC_AI_AGENT_URL=https://agent.example.test:8090
om ai ask "Assess the health of my cluster"
om ai chat
```

For a private agent CA, also set `OPENSVC_AI_AGENT_CA_FILE`.
See the [client guide](docs/om-ai.md) for more commands.

## License

See [LICENSE](LICENSE).
