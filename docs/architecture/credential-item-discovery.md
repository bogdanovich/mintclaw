# Find credential items by website or name

The setup command below supports the earlier fixed-binding path. The new
[live website login](credential-website-login.md) discovers records at browser
runtime and does not require `--bind` or per-site UUID configuration.

Item UUIDs are saved references in local operator configuration, not site rules
or hard-coded values in Go. The private setup command can discover Login items
by website or item name instead of requiring you to copy each UUID yourself.

After the dedicated-vault configuration and private service token are prepared,
use the same `MINTCLAW_HOME` and `MINTCLAW_CONFIG` as the gateway:

```sh
mintclaw credentials find --website https://www.example.com
mintclaw credentials find --name "Example personal"
```

Both filters may be combined. Names match case-insensitive substrings. Websites
match the registrable domain, using the Public Suffix List, including private
suffixes such as `github.io`. Different subdomains can therefore suggest the same
Login. This is candidate discovery, not permission to send a password to every
subdomain. Lookalike suffixes and unrelated private tenants do not match.

Search returns structured JSON containing only item references, names and
website domains. It can run headlessly for the trusted agent; these metadata
fields may appear in model context and logs. The token and credential fields
never appear in this output.

To save a chosen item for an existing alias, use your own unrecorded private
terminal. Disable browser login and stop the affected gateway, then run:

```sh
mintclaw credentials find --website https://www.example.com --bind example
```

Choose the candidate number in that private terminal. Even one result requires
an explicit choice. The command saves its exact item UUID, preserves the
configured vault, allowed origins and principals, and leaves browser login
disabled. Restart the gateway through its normal service workflow. A changed
binding changes the approval policy revision; grants do not survive restart.

The alias's origins, principals and any site login flow must already be prepared
in local configuration. This command selects the item; it does not invent a
site flow or create new access authority. It uses the configured dedicated vault
and the protected token file, with no token argument or environment loader.
Search works while the gateway is running; saving a binding requires it stopped.

Discovery calls the SDK's item-overview list operation, which contains titles,
website metadata, categories and IDs, but no password fields. It never gets full
items, usernames, passwords, OTPs or notes. It returns at most 20 active Login
candidates and projects website metadata to domains, omitting paths and queries.
Binding candidate output and selection stay on the private terminal, outside
normal stdout/stderr. No model-facing secret-value retrieval tool is introduced.

At login time the agent still supplies an alias such as `example`. The runtime
reads the selected item by immutable reference only after approval and live
browser checks. A renamed item keeps its binding; a moved or replaced item needs
another explicit selection. Runtime lookup never guesses among similar names.

1Password's own CLI also accepts item names for `op item get`; its default get
output includes full item data, so it is not used as a model-facing lookup.
See the [official CLI reference](https://www.1password.dev/cli/reference/management-commands/item)
and [SDK item documentation](https://www.1password.dev/sdks/manage-items/).
