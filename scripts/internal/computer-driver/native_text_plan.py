"""Private fixture planning only; preserve complete units, without executing input."""

MAX_ACTION_CHORDS = 32
MAX_ACTIONS = 16
MAX_TOTAL_CHORDS = 256


def plan_complete_units(units):
    actions, pending = [], []
    state = None
    total = 0
    for chords, expected_state in units:
        if not chords or len(chords) > MAX_ACTION_CHORDS:
            raise ValueError("invalid complete fixture unit")
        total += len(chords)
        if total > MAX_TOTAL_CHORDS:
            raise ValueError("fixture plan exceeds chord bound")
        if len(pending) + len(chords) > MAX_ACTION_CHORDS:
            actions.append((tuple(pending), state))
            pending = []
        pending.extend(tuple(chord) for chord in chords)
        state = expected_state
    if pending:
        actions.append((tuple(pending), state))
    if not actions or len(actions) > MAX_ACTIONS:
        raise ValueError("invalid bounded fixture plan")
    return tuple(actions)
