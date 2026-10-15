# Client API

`opensvc-ai-agentd` serves its clients over HTTPS. The OpenSVC webapp is its
client; see the [browser client guide](webapp.md) for the headers, message
history, action confirmation and CORS.

## Backends

The agent serves each backend under its own routes, `/v1/{backend}`. A backend
is one product: its MCP, the identity check of its users and the instructions
given to the model. The route selects the backend; nothing in the request can
select another one.

| Backend | Routes | MCP | Configuration |
|---|---|---|---|
| `om3` | `/v1/om3/...` | OpenSVC daemon MCP, on its local Unix socket | `OPENSVC_AI_OM3_MCP_SOCKET`, required |
| `oc3` | `/v1/oc3/...` | OpenSVC collector MCP, on its local Unix socket | `OPENSVC_AI_OC3_MCP_SOCKET`, optional: the backend is served only when set |

A conversation belongs to the backend it was created on. It is not listed,
read or continued through the routes of another backend.

## Routes

The routes of a backend, here `om3`; `oc3` has the same routes under
`/v1/oc3`:

| Method and path | Purpose |
|---|---|
| `POST /v1/om3/conversations` | Create a conversation |
| `GET /v1/om3/conversations` | List the conversations of the caller |
| `GET /v1/om3/conversations/{id}` | Read a conversation and its pending confirmation |
| `GET /v1/om3/conversations/{id}/messages` | Read its stored messages |
| `PATCH /v1/om3/conversations/{id}` | Rename it |
| `DELETE /v1/om3/conversations/{id}` | Delete it |
| `POST /v1/om3/conversations/{id}/turns` | Send a prompt, streamed |
| `POST /v1/om3/conversations/{id}/turns/{turn_id}/confirmation` | Approve or reject a pending action, streamed |

`GET /health` is shared by all backends and needs no authentication.

Every response carries `X-Request-ID`, the ID the agent gives the request.
Every audit line of the agent carries it as `request_id`, and the agent
forwards it to the MCP of the backend, whose audit lines carry it too: one ID
follows a request from the agent to its tool calls.

`GET /openapi.json` returns the OpenAPI 3.1 description of these routes, their
bodies, errors and stream events, without authentication. The source is
`internal/api/openapi.json`, embedded in the binary; a test keeps its routes
equal to the routes the agent registers.

## Authentication

Each backend declares the headers its routes read. Every `om3` route requires:

```http
Authorization: Bearer <OpenID Connect access token>
X-OpenSVC-Cluster-ID: <ID of the cluster it is intended for>
```

and may add `X-OpenSVC-Node: <node name>`, an optional routing hint read by
om3 only.

The client obtains the token by signing in with the OpenID provider the
daemons of the cluster trust; the om3 MCP accepts no other token. Before
each operation, the agent verifies the token through the om3 MCP
`GET /mcp/auth/whoami`, served only on its local socket: MCP verifies the
token signature, issuer and audience it is configured with, selects the
cluster from the header and relays the token to that daemon's
`GET /api/auth/whoami`, which confirms the identity. The cluster header only
selects the target: a token intended for another cluster is refused by the
selected daemon.

The agent treats the token as opaque. It uses the returned identity and
expiry for conversation ownership and request deadlines, and delegates the
same token and cluster ID to MCP for the tools. The daemon enforces grants.
The token is never stored, logged or given to the model.

The answer of the MCP is reused, in the memory of the agent only, by the next
operations of the same token (and, for om3, the same cluster and node) for at
most one minute and never past its expiry. A refused token is refused again
for five seconds without asking the MCP; an unavailable verification is never
reused. The cache holds identities, never tokens: its keys are hashes. A
change of the account or of the grants on the target therefore applies within
a minute.

Every `oc3` route requires the OpenID Connect access token of the user, and
no other header:

```http
Authorization: Bearer <OpenID Connect access token>
```

Before each operation, the agent verifies the token through the oc3 MCP
`GET /mcp/auth/whoami`, served only on its local socket: MCP verifies the
token signature, issuer and audience it is configured with, relays the token
to the collector's `GET /api/auth/whoami`, and answers the issuer, subject
and expiry the collector confirmed. The agent forwards no other header to the
oc3 MCP, and delegates the same token to it for the tools. The collector
enforces the visibility of the user's account. The
answer is reused as for om3.

## Conversations

A conversation stores the successful turns of the caller, as context for the
next ones. Each turn takes `{"prompt":"..."}` and streams its events as
server-sent events. Only one turn runs at a time in a conversation.

- **Title.** After the first successful turn, the agent derives a title from
  its prompt: whitespace is normalized and titles longer than 80 characters
  are truncated, without an additional model request. A failed first turn
  sets no title. A title set with `PATCH` is not replaced by later prompts.
- **Failures.** Canceled, failed, timed-out and interrupted turns are not
  added to the model context and their partial output is not stored.
- **Tool calls.** A turn runs at most 8 tool calls per model answer and 16 in
  all. Beyond, it fails with the stream `error` event `tool_call_limit`: ask a
  narrower question.
- **Busy.** A second turn while one runs, or while an action waits for a
  decision, returns 409 `conversation_busy`.
- **Expiry.** A conversation expires after its configured lifetime, counted
  from its creation and then from its last completed turn; an expired
  conversation returns 410.

## Identity and isolation

A conversation is bound to its backend and to the verified issuer and subject
of its creator, plus the verified cluster ID for om3; oc3 conversations have no
cluster. Listing returns only the conversations of the
caller on that backend. Reading, renaming or deleting another identity's
conversation returns 404, as for a missing one, without revealing whether it
exists.

Provider credentials remain in the agent configuration and are never returned
through the API.
