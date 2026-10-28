# Client API key upstream restrictions

Client authentication keys and provider API keys are separate credentials. A client key can use all sources (the default), all configured provider API keys, or an explicit selection of provider keys.

Policies live in `access.key-routing`. Each entry contains an opaque `id`, the client `api-key`, a `mode` (`all`, `api-keys`, or `selected`), and, for selected mode only, a nonempty `upstream-ids` list. The existing `access.api-keys` string list remains the client authentication source. A policy must reference a configured client key. Keep its ID when rotating that key and update both fields in the same configuration mutation.

Provider key entries store a persistent `routing-id`. Keep this ID when editing or rotating the existing credential. New credentials must get a new ID. Removing a provider key does not remove its reference from client policies: an exhausted selection returns an error and never becomes unrestricted.

`POST /v8/management/access/upstream-keys/prepare` initializes missing provider identities and returns a catalog containing only ID, provider, connection name, and masked key. The operation requires management authentication, is idempotent, and persists only identity initialization when needed. Policy writes use the v8 configuration API at `access/key-routing`, or an atomic root configuration patch when changing client keys too. New unknown upstream references are rejected; existing deleted references can be retained or removed.

Restricted modes admit only credentials with source `config` and kind `apikey`. Session affinity, pinned credentials, scheduler plugins, and credential retries remain inside this set. Client headers cannot override the policy. Each request snapshots its policy after authentication; subsequent requests receive the current policy after configuration reload. Native Realtime client secrets inherit their issuer's current policy.

Home dispatch cannot transport this restriction in its current protocol. Restricted requests fail with `routing_policy_unsupported` rather than falling back to an unrestricted remote selection. Existing Home selections reused by live protocols are checked locally as well. All-source clients retain their previous behavior.

The management UI requires this backend change to edit restrictions. Unsupported backends leave the routing control disabled and show an explanation. Older configuration editors cannot silently remove a restricted policy while retaining its client key; explicitly write mode `all` to remove the restriction. Configuration access is administrative and may deliberately change policies.
