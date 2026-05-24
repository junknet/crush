Load JSON Schemas for tools whose schemas have been deferred to keep the
initial tool list small. Returns a `<functions>` block containing the full
schema of every matched tool — once loaded, these tools become directly
callable in the same conversation.

Query syntax:

- `select:NameA,NameB` — load these exact tools by name. No ranking; every
  named tool that exists in the deferred registry is returned. Use this when
  the prior turn (or a `<system-reminder>`) already told you the names.
- `+term keyword keyword` — keyword search. Tokens prefixed with `+` are
  required (a tool that does not contain the term in its name, description
  or search hint will not match). Remaining tokens are scored. Ranking:
  name match weight 12, search-hint match 4, description match 2. Top
  `max_results` (default 5) tools are returned.

The response also lists any MCP servers that are still connecting under
`<connecting_mcp_servers>` — those servers' tools will appear in a later
`<system-reminder>` once they finish initializing.

After this call, simply invoke the returned tool by its `name`; the model
runtime will route the call to the real implementation rather than the
deferred stub.
