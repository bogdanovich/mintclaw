package document

import "reflect"

// Pending routing messages and protected question replies are admitted before
// they become field inputs. Their order and original question must not depend
// on how long private interpretation takes. No timestamp heuristic is used.
const MaxFormIntakeAdmissionOrder = MaxFormIntakeReceipts * (2 + maxFormTaskStoredAnswers)

func formIntakeAdmissionOrder(mode *formIntakeStoredMode, index int) int {
	if index < 0 || index >= len(mode.Inputs) {
		return 0
	}
	if order := mode.Inputs[index].AdmissionOrder; order > 0 {
		return order
	}
	return index + 1 // Legacy, append-only field inputs need no migration.
}

func nextFormIntakeAdmissionOrder(mode *formIntakeStoredMode) int {
	order := 0
	for index := range mode.Inputs {
		order = max(order, formIntakeAdmissionOrder(mode, index))
	}
	for _, route := range mode.TaskRoutes {
		order = max(order, route.AdmissionOrder)
		for _, answer := range route.Answers {
			order = max(order, answer.AdmissionOrder)
		}
	}
	return order + 1
}

func routedFormIntakeOrigin(
	jobID string,
	mode *formIntakeStoredMode,
	reference string,
) (FormIntakeContext, int, error) {
	for _, route := range mode.TaskRoutes {
		if formTaskRoutePrefix+jobID+"."+route.ID == reference {
			return cloneFormIntakeContext(route.Context), route.AdmissionOrder, nil
		}
		for _, answer := range route.Answers {
			if formTaskAnswerReference(jobID, route.ID, answer.ID) == reference {
				return cloneFormIntakeContext(answer.Context), answer.AdmissionOrder, nil
			}
		}
	}
	return FormIntakeContext{}, 0, ErrFormJobAnswerConflict
}

func isRoutedFormIntakeInput(jobID string, mode *formIntakeStoredMode, inputID string) bool {
	reference := formIntakeReceiptPrefix + jobID + "." + inputID
	for _, route := range mode.TaskRoutes {
		if route.FieldReference == reference {
			return true
		}
		for _, answer := range route.Answers {
			if answer.FieldReference == reference {
				return true
			}
		}
	}
	return false
}

func validateFormIntakeAdmissionOrders(mode *formIntakeStoredMode) error {
	seen := make(map[int]string)
	linked := make(map[string]bool)
	validateLink := func(reference string, order int, origin FormIntakeContext) (string, bool) {
		if reference == "" {
			return "", true
		}
		_, id, err := parseFormIntakeReceipt(reference)
		if err != nil || linked[id] {
			return "", false
		}
		for index, input := range mode.Inputs {
			if input.ID == id {
				if formIntakeAdmissionOrder(mode, index) != order || !reflect.DeepEqual(input.Origin.Context, origin) {
					return "", false
				}
				linked[id] = true
				return id, true
			}
		}
		return "", false
	}
	add := func(order int, fieldReference string) bool {
		if order < 1 || order > MaxFormIntakeAdmissionOrder {
			return false
		}
		if prior, exists := seen[order]; exists {
			return fieldReference != "" && prior == fieldReference
		}
		seen[order] = fieldReference
		return true
	}
	for index, input := range mode.Inputs {
		if input.AdmissionOrder < 0 || !add(formIntakeAdmissionOrder(mode, index), input.ID) {
			return ErrFormJobRecordCorrupt
		}
	}
	for _, route := range mode.TaskRoutes {
		fieldID, valid := validateLink(route.FieldReference, route.AdmissionOrder, route.Context)
		if !valid || !validFormIntakeContext(route.Context) || !add(route.AdmissionOrder, fieldID) {
			return ErrFormJobRecordCorrupt
		}
		for _, answer := range route.Answers {
			fieldID, valid = validateLink(answer.FieldReference, answer.AdmissionOrder, answer.Context)
			if !valid || !validFormIntakeContext(answer.Context) || !add(answer.AdmissionOrder, fieldID) {
				return ErrFormJobRecordCorrupt
			}
		}
	}
	return nil
}
