package tasks

import "github.com/bogdanovich/mintclaw/pkg/taskresult"

func (r *Registry) eventsSinceLocked(start int) []TaskEvent {
	if start < 0 || start >= len(r.events) {
		return nil
	}
	return append([]TaskEvent(nil), r.events[start:]...)
}

func cloneTaskRecord(record Record) Record {
	cloned := record
	cloned.Deliverable = taskresult.CloneDeliverable(record.Deliverable)
	return cloned
}

func cloneTaskEvent(event TaskEvent) TaskEvent {
	cloned := event
	cloned.Payload = copyStringMap(event.Payload)
	return cloned
}
