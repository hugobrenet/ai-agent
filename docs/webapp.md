# OpenID client headers

Each protected HTTPS request supplies the user's access token, cluster ID and
daemon node name:

```http
Authorization: Bearer <OpenID access_token>
X-OpenSVC-Cluster-ID: <cluster.config.id>
X-OpenSVC-Node: <daemon.nodename>
```

Use these headers for conversation creation, listing, reading, renaming,
deletion and turns, as well as one-shot asks. The request bodies and SSE
events are unchanged: a turn still sends only `{"prompt":"..."}`. The target
belongs in the header so authentication completes before reading the body.
Use the current token after renewal; the agent does not refresh tokens.

Each target header is one exact, nonempty value of at most 256 bytes, without
surrounding whitespace, control characters or commas. Duplicate headers are
rejected. Both are required for OpenID: the MCP resolves the exact cluster/node
pair in its administrator-owned catalogue, never a client-supplied URL or DNS
lookup. Missing or unknown targets are refused without fallback.
Native `om ai` requests still work without either target header. If supplied
with a native JWT, they must match its `cluster_id` and `iss`, respectively.

## Verification and forwarding

The agent checks structure, required `iss/sub/aud/exp`, optional `nbf`, a
nonempty `kid`, and asymmetric algorithms RS256/384/512, PS256/384/512 or
ES256/384/512. OpenID tokens in this profile do not carry the native
`cluster_id` or `token_use` markers: tokens carrying either are subject to
native checks and never fall back to OpenID after a refusal.

These checks do not establish authenticity. The agent forwards the exact
Bearer and both target headers to its configured MCP's `GET /mcp/auth/whoami`.
The MCP must validate through the target daemon, including the expected
authentication strategy, signature, issuer and audience, and return:

```json
{
  "cluster_id": "<requested cluster ID>",
  "issuer": "<JWT iss>",
  "subject": "<JWT sub>",
  "expires_at": "<JWT exp as RFC3339>"
}
```

The agent requires an exact match, including the opaque OpenID `sub`, not
`preferred_username`. Invalid credentials or identity mismatches return 401;
unavailable verification returns 503. No conversation or model access is
allowed before this check. Conversation ownership remains cluster + issuer
+ subject. Each subsequent MCP request carries the same Bearer and targets
from private request context; none is exposed to the model or stored as
a credential. TLS verification and origin binding apply to all headers.

## CORS

Set `OPENSVC_AI_CORS_ALLOWED_ORIGINS` in the agent environment and restart:

```dotenv
# Restrictive: allow these webapp origins only.
OPENSVC_AI_CORS_ALLOWED_ORIGINS=https://webapp-a.example:1215,https://webapp-b.example:1215

# Alternatively: allow every browser origin.
OPENSVC_AI_CORS_ALLOWED_ORIGINS=*
```

Empty or unset disables CORS. Origins contain the scheme, hostname and optional
port, without `/ui`, a trailing slash, credentials, query or fragment. HTTP
origins are accepted for local frontend development; the agent API still uses
HTTPS. Hostnames are normalized to lowercase and default ports are omitted.
Partial wildcards are not supported, and `*` cannot be mixed with a list.
Malformed configuration prevents startup, without echoing its contents.

The browser client must use `fetch` with `credentials: "omit"` and supply its
Bearer token explicitly. The agent never sets `Access-Control-Allow-Credentials`.
Open mode permits use from any website; it does not relax JWT verification,
cluster/node catalogue restrictions, daemon grants or TLS checks. Use exact
origins when they are known and controlled.

Preflight `OPTIONS` requests are handled before JWT authentication. Allowed
methods are `GET`, `POST`, `PATCH` and `DELETE`; allowed request headers are
`Authorization`, `Content-Type`, `X-OpenSVC-Cluster-ID` and `X-OpenSVC-Node`.
Successful preflights return 204 and may be cached by the browser for 600
seconds. Actual requests still pass through normal authentication, and allowed
origins receive CORS headers on errors and SSE responses too. Unlisted origins
in restrictive mode return 403; invalid preflight methods/headers also return
403 without invoking authentication, conversation storage or the model.

Clients without `Origin`, including `om ai`, are unaffected. CORS is a browser
policy, not an authentication mechanism or protection against non-browser
clients. No CORS headers or origin registrations are sent to MCP or daemons.

## Integration status

This is the agent-side OpenSVC delegation contract, not generic MCP OAuth
discovery or token exchange. The MCP must implement OpenID routing and the
identity bridge described above before an actual OpenID request can succeed.
Browser cross-origin access is configurable as described above. The webapp
chatbot page is separate client work.
