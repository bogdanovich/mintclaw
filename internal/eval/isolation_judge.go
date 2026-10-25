package eval

// Only visible conversation is projected for judging, never the native proof.
// Namespaces keep both real per-connection event ID sequences unambiguous.
func steeringJudgeEvents(trial Trial) []Event {
	if trial.Isolation == nil {
		return trial.Events
	}
	result := make([]Event, 0, len(trial.Events)+len(trial.Isolation.Peer.Events))
	appendScope := func(label string, source []Event) {
		for _, event := range source {
			event.ID = "scope-" + label + "-" + event.ID
			if event.Content != "" {
				event.Content = "[scope " + label + "] " + event.Content
			}
			result = append(result, event)
		}
	}
	appendScope("a", trial.Events)
	appendScope("b", trial.Isolation.Peer.Events)
	return result
}
