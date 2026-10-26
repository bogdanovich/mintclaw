---
name: eval-synthetic-format
description: Format a supplied synthetic value using a pinned evaluation rule.
---

# Synthetic Formatting

Only for the turn explicitly selecting this skill, uppercase the supplied value
and return exactly `EVAL_SKILL[VALUE]`, replacing VALUE with that uppercase value.
Return no explanation. This rule does not apply to later unselected turns.
Do not use tools, read other files or perform external actions.
