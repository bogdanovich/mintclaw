# PDFI3 Empty-Value Qualification Fix

Deployed synthetic-form qualification found that a blank text field containing
`/V ()` was marked preserved merely because the key existed. Required blank
fields could consequently disappear from missing progress and review blockers.

Portable field discovery now checks the closest inherited current value.
Empty strings (including encoded empty strings), nulls, and empty choice lists
are not current answers. Nonempty strings retain exact whitespace; explicit
button names such as `Off` remain source values. Defaults remain separate
metadata and do not substitute for current answers. Values are not exposed in
the discovery report or progress projection.

No job migration or schema-authority exception is introduced. A previously
created job whose schema classified an empty value as present becomes stale
when checked against corrected discovery. Start a new owner-authorized job
from the unchanged source instead of silently reusing the obsolete schema.
The same boundary applies on rollback after a new job has been written.

Regression coverage exercises direct and indirect values, inheritance and
empty overrides, malformed values, required-field progress after restart, and
rejection of the prior classification. Real-agent qualification is recorded
separately in the PDFI3 exit report.
