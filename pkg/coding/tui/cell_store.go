package tui

import (
	"errors"
	"fmt"
	"strings"

	"github.com/bogdanovich/mintclaw/pkg/coding/frontend"
)

var errStaleSemanticCellRevision = errors.New("stale semantic cell revision")

// semanticCellStore is a disposable renderer index over the authoritative
// bounded frontend snapshot. It owns immutable cells, never conversation or
// runtime state, and can be rebuilt from ThreadSnapshot.Items at any time.
type semanticCellStore struct {
	ordered   []*presentationCell
	committed []*presentationCell
	active    []*presentationCell
	byID      map[string]*presentationCell
}

func newSemanticCellStore(items []frontend.PresentationItem) (semanticCellStore, error) {
	return reconcileSemanticCellStore(semanticCellStore{}, items)
}

func reconcileSemanticCellStore(
	previous semanticCellStore,
	items []frontend.PresentationItem,
) (semanticCellStore, error) {
	next := semanticCellStore{
		ordered: make([]*presentationCell, 0, len(items)),
		byID:    make(map[string]*presentationCell, len(items)),
	}
	var priorSequence uint64
	for index, item := range items {
		if err := validateSemanticCellItem(item); err != nil {
			return semanticCellStore{}, fmt.Errorf("presentation item %d: %w", index, err)
		}
		if index != 0 && item.Sequence <= priorSequence {
			return semanticCellStore{}, fmt.Errorf(
				"presentation item %q sequence %d is not after %d",
				item.ID,
				item.Sequence,
				priorSequence,
			)
		}
		priorSequence = item.Sequence
		if _, duplicate := next.byID[item.ID]; duplicate {
			return semanticCellStore{}, fmt.Errorf("duplicate presentation item ID %q", item.ID)
		}

		cell, err := reconcileSemanticCell(previous.byID[item.ID], item)
		if err != nil {
			return semanticCellStore{}, err
		}
		next.ordered = append(next.ordered, cell)
		next.byID[item.ID] = cell
		if presentationCellCommitted(item.Lifecycle) {
			next.committed = append(next.committed, cell)
		} else {
			next.active = append(next.active, cell)
		}
	}
	return next, nil
}

func reconcileSemanticCell(
	current *presentationCell,
	item frontend.PresentationItem,
) (*presentationCell, error) {
	if current == nil {
		return newPresentationCell(item), nil
	}
	identity := current.Identity()
	if item.Sequence != identity.Sequence {
		return nil, fmt.Errorf(
			"presentation item %q changed stable sequence from %d to %d",
			item.ID,
			identity.Sequence,
			item.Sequence,
		)
	}
	if item.Kind != identity.Kind {
		return nil, fmt.Errorf(
			"presentation item %q changed stable kind from %q to %q",
			item.ID,
			identity.Kind,
			item.Kind,
		)
	}
	if item.TurnID != current.item.TurnID {
		return nil, fmt.Errorf(
			"presentation item %q changed stable turn from %q to %q",
			item.ID,
			current.item.TurnID,
			item.TurnID,
		)
	}
	if item.Revision < identity.Revision {
		return nil, fmt.Errorf(
			"%w for %q: got %d after %d",
			errStaleSemanticCellRevision,
			item.ID,
			item.Revision,
			identity.Revision,
		)
	}
	if item.Revision == identity.Revision {
		return current, nil
	}
	return newPresentationCell(item), nil
}

func validateSemanticCellItem(item frontend.PresentationItem) error {
	if strings.TrimSpace(item.ID) == "" {
		return errors.New("presentation item ID is required")
	}
	if item.Sequence == 0 {
		return fmt.Errorf("presentation item %q has zero sequence", item.ID)
	}
	if item.Revision == 0 {
		return fmt.Errorf("presentation item %q has zero revision", item.ID)
	}
	if !knownPresentationLifecycle(item.Lifecycle) {
		return fmt.Errorf("presentation item %q has unknown lifecycle %q", item.ID, item.Lifecycle)
	}
	payloads := 0
	if item.Message != nil {
		payloads++
	}
	if item.Tool != nil {
		payloads++
	}
	if item.Plan != nil {
		payloads++
	}
	if payloads != 1 {
		return fmt.Errorf("presentation item %q has %d typed payloads", item.ID, payloads)
	}
	switch item.Kind {
	case frontend.PresentationUserMessage,
		frontend.PresentationAssistantMessage,
		frontend.PresentationReasoning,
		frontend.PresentationToolMessage,
		frontend.PresentationWarning,
		frontend.PresentationError:
		if item.Message == nil {
			return fmt.Errorf("presentation item %q kind %q requires a message payload", item.ID, item.Kind)
		}
	case frontend.PresentationToolCall:
		if item.Tool == nil {
			return fmt.Errorf("presentation item %q kind %q requires a tool payload", item.ID, item.Kind)
		}
	case frontend.PresentationPlanUpdate:
		if item.Plan == nil {
			return fmt.Errorf("presentation item %q kind %q requires a plan payload", item.ID, item.Kind)
		}
	default:
		return fmt.Errorf("presentation item %q has unsupported kind %q", item.ID, item.Kind)
	}
	return nil
}

func knownPresentationLifecycle(lifecycle frontend.PresentationLifecycle) bool {
	switch lifecycle {
	case frontend.PresentationActive,
		frontend.PresentationCompleted,
		frontend.PresentationFailed,
		frontend.PresentationInterrupted,
		frontend.PresentationSuspended,
		frontend.PresentationUnknown:
		return true
	default:
		return false
	}
}

func presentationCellCommitted(lifecycle frontend.PresentationLifecycle) bool {
	switch lifecycle {
	case frontend.PresentationCompleted, frontend.PresentationFailed, frontend.PresentationInterrupted:
		return true
	default:
		return false
	}
}

func (store semanticCellStore) cells() []semanticCell {
	cells := make([]semanticCell, len(store.ordered))
	for index, cell := range store.ordered {
		cells[index] = cell
	}
	return cells
}

func cloneCellPresentationItem(item frontend.PresentationItem) frontend.PresentationItem {
	return frontend.ThreadSnapshot{Items: []frontend.PresentationItem{item}}.Clone().Items[0]
}
