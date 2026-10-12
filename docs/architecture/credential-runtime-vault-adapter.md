# Runtime-Internal 1Password Adapter

The [trusted-runtime design](credential-trusted-runtime-design.md) uses one
private 1Password adapter inside the existing application. The implementation
now lives at `internal/credentials/vault`, accessible to trusted gateway/runtime
code rather than restricted to the old standalone command's internal imports.
This is a relocation of the existing adapter, not another secret manager.

The official SDK remains pinned at the repository's existing version. Retrieval
uses an exact configured vault/item binding and validates the returned login
item. Username/password buffers are lent to one synchronous consumer and then
cleared. SDK/string/browser copies cannot be guaranteed erased. Formatting and
serialization of private login/provider objects reveal fixed placeholders.
Provider and consumer failures produce fixed errors, with no SDK diagnostics.

The adapter does not read environment tokens, enroll a phone, issue a grant,
create a model tool, activate login, or register a browser session. The integrated
coordinator must enforce approval/current scope, register values with the
runtime output guard before use, and suppress browser/connector output at its
source. Moving a package does not establish those gates as complete.

The old inactive browser consumer imports the relocated adapter while remaining
appliance scaffolding is retired. Existing adapter and private-channel tests
continue to cover exact retrieval, field validation, single-use consumption,
buffer cleanup, fixed errors, and safe formatting; no real token is used.
