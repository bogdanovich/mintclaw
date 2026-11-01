# Native Browser Reference Regions

The direct Playwright library driver can attach the nearest uniquely named ARIA
region to a main-document button reference. This is contextual evidence, not a
permission, account identity, or a semantic interpretation of page text.

## Contract

- The driver projects the existing structured accessibility tree, not rendered
  YAML indentation, button captions, or positional guesses.
- The private `Reference regions` header uses versioned, strict JSON. It is not
  included in the model-visible snapshot. The broker retains optional
  `element_region_name` and `destination_element_region_name` in prepared actions.
- Projection is bounded to 4 KiB, 32 button entries, 512 UTF-8 bytes per name,
  4096 tree nodes and depth 32. Duplicate region names, unsupported references,
  missing names, incomplete trees and truncated observations cannot qualify a
  region. Iframe and unnamed-region coverage is not claimed.
- Previously qualified refs request a fresh full-document structured projection
  during targeted resolution. The cached name selects observation detail only;
  the fresh tree supplies the result. A moved control, changed name, duplicate
  name or lost context invalidates the earlier prepared binding.
- Existing action hashing and approval HMACs bind the optional fields. Existing
  preparation/dispatch revalidation compares them. Approval summaries quote the
  region as untrusted text. Policy does not derive permission from the name.
- Empty fields are omitted, preserving legacy empty-context JSON/hash inputs.
  Older drivers remain unqualified rather than inferring missing context.
  Downgrading persisted nonempty-context actions is not qualified.

## Eval Boundary

This producer does not enable `browser.fixture.ambiguous`. That consumer must
independently join the immutable owned fixture, exact origin/path, native owner,
current snapshot, resolved user clarification and exact native allow-once
approval, then inspect the actual post-stop fixture state. A region name alone
must never authorize account removal. Companion/cloud parity and a new browser
or approval engine are outside this change.

Model-free controls cover structured projection bounds, strict parsing, cached
versus fresh resolution, action storage/hash/revalidation, quoted approvals, and
actual Chrome DOM reparenting/duplicate regions. They are not live eval results.
