# OpenID client headers

Each protected HTTPS request supplies the user's access token and cluster ID:

```http
Authorization: Bearer <OpenID access_token>
X-OpenSVC-Cluster-ID: <cluster.config.id>
```

Use these headers for conversation creation, listing, reading, renaming,
deletion and turns, as well as one-shot asks. The request bodies and SSE
events are unchanged: a turn still sends only `{"prompt":"..."}`. The target
belongs in the header so authentication completes before reading the body.
Use the current token after renewal; the agent does not refresh tokens.

The target is one exact, nonempty value of at most 256 bytes, without surrounding
whitespace, control characters or commas. Duplicate headers are rejected.
Native `om ai` requests still work without this header. If supplied with a
native JWT, it must match the JWT's `cluster_id`.

## Verification and forwarding

The agent checks structure, required `iss/sub/aud/exp`, optional `nbf`, a
nonempty `kid`, and asymmetric algorithms RS256/384/512, PS256/384/512 or
ES256/384/512. OpenID tokens in this profile do not carry the native
`cluster_id` or `token_use` markers: tokens carrying either are subject to
native checks and never fall back to OpenID after a refusal.

These checks do not establish authenticity. The agent forwards the exact
Bearer and target header to its configured MCP's `GET /mcp/auth/whoami`.
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
+ subject. Each subsequent MCP request carries the same Bearer and target
from private request context; neither is exposed to the model or stored as
a credential. TLS verification and origin binding apply to both headers.

## Integration status

This is the agent-side OpenSVC delegation contract, not generic MCP OAuth
discovery or token exchange. The MCP must implement OpenID routing and the
identity bridge described above before an actual OpenID request can succeed.
Browser cross-origin access also requires a separate CORS configuration;
this change does not add it. The agent accepts HTTPS clients for verification
of this contract independently of the future webapp page.
