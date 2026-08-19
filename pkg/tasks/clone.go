package tasks

func (r *Registry) eventsSinceLocked(start int) []TaskEvent {
	if start < 0 || start >= len(r.events) {
		return nil
	}
	return append([]TaskEvent(nil), r.events[start:]...)
}

func cloneTaskRecord(record Record) Record {
	cloned := record
	if record.Completion != nil {
		completion := *record.Completion
		completion.Media = append([]CompletionMedia(nil), record.Completion.Media...)
		completion.ObjectiveOutcome = cloneObjectiveOutcome(record.Completion.ObjectiveOutcome)
		cloned.Completion = &completion
	}
	if record.Deliverable != nil {
		deliverable := *record.Deliverable
		deliverable.Artifacts = append([]DeliverableItem(nil), record.Deliverable.Artifacts...)
		deliverable.Metadata = copyStringMap(record.Deliverable.Metadata)
		deliverable.Report = cloneDeliverableReport(record.Deliverable.Report)
		deliverable.ObjectiveOutcome = cloneObjectiveOutcome(record.Deliverable.ObjectiveOutcome)
		cloned.Deliverable = &deliverable
	}
	return cloned
}

func cloneObjectiveOutcome(outcome *ObjectiveOutcome) *ObjectiveOutcome {
	if outcome == nil {
		return nil
	}
	cloned := &ObjectiveOutcome{
		Status: outcome.Status, MissingItems: append([]string(nil), outcome.MissingItems...),
	}
	for _, item := range outcome.CompletedItems {
		clonedItem := ObjectiveItem{Item: item.Item, Kind: item.Kind}
		for _, receipt := range item.Receipts {
			receipt.Metadata = copyStringMap(receipt.Metadata)
			clonedItem.Receipts = append(clonedItem.Receipts, receipt)
		}
		cloned.CompletedItems = append(cloned.CompletedItems, clonedItem)
	}
	return cloned
}

func cloneTaskEvent(event TaskEvent) TaskEvent {
	cloned := event
	cloned.Payload = copyStringMap(event.Payload)
	return cloned
}
