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
| `PUT /v1/om3/conversations/{id}/turns/{turn_id}/feedback` | Give or replace the feedback on a turn |
| `DELETE /v1/om3/conversations/{id}/turns/{turn_id}/feedback` | Withdraw it |

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

## Tool search

When the MCP of a backend lists more tools than
`OPENSVC_AI_TOOL_SEARCH_THRESHOLD` (20 by default), or tools too large for
one model request (1 MiB), the model does not see them all. A threshold of
`-1` searches only such a large catalog. The agent accepts catalogs of up to
1024 tools and 4 MiB, the size of one MCP response. It sees `search_tools`, a tool of the agent, and an active set:

- the core tools: those the MCP marks with `"opensvc/core": true` in their
  `_meta`, else the default list of the backend;
- the tools called in the recent history of the conversation, at most 8;
- the tools its searches found during the turn.

`search_tools` takes a query and an optional limit (5 by default, 10 at
most). Its description, the description of its query and the system prompt
ask the model for 3 to 8 English keywords describing the required
capability, without greetings, full sentences or resource identifiers: a
whole question brings words that favor the wrong tools. The query is bounded
to 512 UTF-8 bytes (its schema can only state 512 characters as
`maxLength`), its first 32 distinct terms are searched, and a query without
a searchable term is refused. The agent ranks the catalog with BM25 over the name, title,
description, parameters and `opensvc/keywords` metadata of each tool, and
returns the best tools with the first sentence of their description; they
are shown from the next model call of the turn. When the tools shown would
exceed the size of a model request, the oldest tools that are not core make
room, those of the history first; a tool that still does not fit is
returned with `shown` false. A turn runs at most 3
valid searches, counted in its tool calls; a refused search is a tool error
for the model and does not count. A turn suspended while searching keeps
searching when it resumes. A tool of the catalog the model calls without having been shown it
runs as any other, with its annotations and confirmation. The searches are
stored with the tool calls of the turn, so that a feedback shows them, and
a turn resumed after a confirmation sees the tools it found.



## History compaction

The model receives the earlier turns of a conversation as a compact copy;
the stored messages, the feedback and the export keep the original results.
The tool calls, their arguments and call IDs are kept. Small results, errors
and results without structured content are kept as they are. A structured
result over 2 KiB is compacted to at most 2 KiB: its scalars and small
objects of scalars, such as a provenance, are kept, numbers as written and
strings unchanged; a string over 256 bytes is omitted whole; collections
keep their first items, 8 at first, fewer until the result fits. Beyond
16 KiB of tool results in the history, the oldest results are reduced
further, down to a marker alone, and as a last resort the oldest turns are
left out whole.

A compacted result carries a `_history_compaction` field: the policy
(`v1`), `partial`, the original size, and its omissions, at most 16, each a
JSON pointer into the result with its kind (`array_tail`, `object_entries`,
`long_string`, `depth`, `content`, `value`) and the items kept and omitted;
no artificial item is added to the data. The system prompt tells the model
that omitted data is unknown, to read omitted details again with a read-only
tool, never again with a tool that changes state, and that a new read is a
new observation. Each run records the policy, its budgets, the sizes of the
history loaded, compacted and sent, the results compacted and the turns left
out.

## Feedback

The user rates a completed turn, to help review and improve the tools and the
prompts. `PUT …/turns/{turn_id}/feedback` takes:

```json
{"rating": "down", "reasons": ["missing_information"], "expected": "The number of nodes per OS"}
```

- `rating` is `up` or `down`.
- `reasons`, only with `down`, optional, any of `not_answered`,
  `missing_information`, `incorrect_or_outdated`, `hard_to_understand` and
  `action_not_done`.
- `expected`, optional, is what the user would have expected: at most 1000
  characters, kept as untrusted text.

A turn has one feedback: a new `PUT` replaces its rating, reasons and expected
text, and `DELETE` withdraws it. Only the owner of the conversation can rate
its turns; a turn still running or failed is refused with 400
`invalid_feedback`. The assistant messages of `GET …/messages` carry the
feedback of their turn, for the client to show it again.

The first feedback of a turn copies it for review: the question, the answer,
and the tool calls with their arguments, error flag and the start of their
results, each truncated. It also records what executed the turn, as the
turn recorded it while it ran: the agent version, the model, a hash of the
system prompt actually sent, whether the tool catalog was searched and the
tools shown to the model, and the MCP server, with the name and version it announced
and a hash of the tools it listed, which changes with any tool name,
description, schema or annotation. A turn resumed after a confirmation on a
different agent or MCP records one run per execution. The feedback holds no
identity: it stays linked to its turn while the conversation exists, for the
user to change it, and is then kept anonymous
until `OPENSVC_AI_FEEDBACK_RETENTION` (180 days by default) ends. The
context is bounded to 16 KiB: beyond, it leaves out the shown tools of the
runs, counted in `shown_tools_omitted`, then the runs between the first and
the last, counted in `runs_omitted`, and sets `truncated`. The agent
keeps at most 4096 feedback; beyond, a new one returns 409 `feedback_limit`.

`opensvc-ai-agentd feedback export [-since 720h] [-with-tool-results]`, run on
the agent host with its environment, writes the feedback created within the
duration as JSON lines, without conversation or turn IDs. The tool results,
which may hold inventory data, are left out unless `-with-tool-results` is
given. Sending the export to OpenSVC is the choice of the operator.

## Identity and isolation

A conversation is bound to its backend and to the verified issuer and subject
of its creator, plus the verified cluster ID for om3; oc3 conversations have no
cluster. Listing returns only the conversations of the
caller on that backend. Reading, renaming or deleting another identity's
conversation returns 404, as for a missing one, without revealing whether it
exists.

Provider credentials remain in the agent configuration and are never returned
through the API.
