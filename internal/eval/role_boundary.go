package eval

import (
	"context"
	"errors"
	"reflect"
)

const roleBoundaryScope = "typed_provider_arguments_and_response_v1"

var errRoleBoundaryUnavailable = errors.New("eval: bounded role boundary evidence unavailable")

type RoleBoundaryObservation struct {
	Scope     string `json:"scope"`
	FactScope string `json:"fact_scope"`
	Input     string `json:"input"`
	Output    string `json:"output"`
}

func (observation *RoleBoundaryObservation) valid(attempted bool) bool {
	if observation == nil {
		return true
	}
	if observation.Scope != roleBoundaryScope ||
		observation.FactScope != "run_retained_facts" && observation.FactScope != "call_facts" &&
			observation.FactScope != "credentials_only" && observation.FactScope != generatedFactGuardScope {
		return false
	}
	validInput := observation.Input == "not_observed" || observation.Input == "observed" ||
		observation.Input == "unavailable" || observation.Input == "leak_observed"
	validOutput := observation.Output == "not_observed" || observation.Output == "not_returned" ||
		observation.Output == "observed" || observation.Output == "unavailable" || observation.Output == "leak_observed"
	return validInput && validOutput &&
		(!attempted || observation.Input == "observed") &&
		(!attempted || observation.Output != "not_observed") &&
		(attempted || observation.Output == "not_observed")
}

func roleBoundaryEligible(call *RoleCall) bool {
	if call == nil || call.Boundary == nil {
		return true // Legacy evidence gains no new boundary coverage.
	}
	return call.Boundary.valid(call.Attempted) && call.Boundary.Input == "observed" &&
		(call.Boundary.Output == "observed" || call.Boundary.Output == "not_returned")
}

func validTrialRoleBoundaries(trial Trial) bool {
	for _, call := range trial.DriverCalls {
		if !call.Boundary.valid(call.Attempted) {
			return false
		}
	}
	return trial.Judge == nil || trial.Judge.Call == nil || trial.Judge.Call.Boundary.valid(trial.Judge.Call.Attempted)
}

// Walk the typed provider value without marshaling arbitrary tool arguments or
// invoking String/MarshalJSON methods. Limits also bound cycles and wide maps.
type roleValueInspection struct {
	ctx         context.Context
	scans       []*privateContentScan
	nodes, size int
	uncertain   bool
}

func (role *ModelRole) inspectBoundary(ctx context.Context, value any, facts map[string]string) error {
	inspection := roleValueInspection{ctx: ctx}
	inspection.scans = append(inspection.scans, &privateContentScan{facts: facts, filter: role.filter, tokens: 32768})
	if role.retained != nil {
		inspection.scans = append(inspection.scans,
			&privateContentScan{facts: role.retained.values, generated: role.retained.generated, tokens: 32768})
	}
	err := inspection.walk(reflect.ValueOf(value), 0)
	if err == nil && inspection.uncertain {
		return errRoleBoundaryUnavailable
	}
	return err
}

func (inspection *roleValueInspection) text(value string) error {
	if len(value) > MaxInputBytes-inspection.size {
		return errRoleBoundaryUnavailable
	}
	inspection.size += len(value)
	for _, scan := range inspection.scans {
		scan.uncertain = false
		if !scan.text(value, 0) {
			continue
		}
		if !scan.uncertain {
			return ErrPrivateEvidence
		}
		// Unframed escaped content is still ineligible. A known canary revealed
		// by raw JSON-unescaping is a positive leak, not merely uncertainty.
		if scan.rawEscapes(inspection.ctx, []byte(value)) && !scan.uncertain {
			return ErrPrivateEvidence
		}
		inspection.uncertain = true
	}
	return nil
}

func (inspection *roleValueInspection) walk(value reflect.Value, depth int) error {
	inspection.nodes++
	if inspection.ctx == nil || inspection.ctx.Err() != nil || depth > 24 || inspection.nodes > 4096 {
		return errRoleBoundaryUnavailable
	}
	if !value.IsValid() {
		return nil
	}
	switch value.Kind() {
	case reflect.Interface, reflect.Pointer:
		if value.IsNil() {
			return nil
		}
		return inspection.walk(value.Elem(), depth+1)
	case reflect.String:
		return inspection.text(value.String())
	case reflect.Struct:
		for index := 0; index < value.NumField(); index++ {
			if !value.Field(index).CanInterface() {
				return errRoleBoundaryUnavailable
			}
			if err := inspection.walk(value.Field(index), depth+1); err != nil {
				return err
			}
		}
	case reflect.Map:
		if value.Type().Key().Kind() != reflect.String || value.Len() > 4096-inspection.nodes {
			return errRoleBoundaryUnavailable
		}
		iterator := value.MapRange()
		for iterator.Next() {
			if err := inspection.walk(iterator.Key(), depth+1); err != nil {
				return err
			}
			if err := inspection.walk(iterator.Value(), depth+1); err != nil {
				return err
			}
		}
	case reflect.Array, reflect.Slice:
		if value.Type().Elem().Kind() == reflect.Uint8 {
			if value.Len() > MaxInputBytes-inspection.size {
				return errRoleBoundaryUnavailable
			}
			if value.Kind() == reflect.Slice {
				return inspection.text(string(value.Bytes()))
			}
			data := make([]byte, value.Len())
			defer clear(data)
			for index := range data {
				data[index] = byte(value.Index(index).Uint())
			}
			return inspection.text(string(data))
		}
		if value.Len() > 4096-inspection.nodes {
			return errRoleBoundaryUnavailable
		}
		for index := 0; index < value.Len(); index++ {
			if err := inspection.walk(value.Index(index), depth+1); err != nil {
				return err
			}
		}
	case reflect.Bool, reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Float32, reflect.Float64:
	default:
		return errRoleBoundaryUnavailable
	}
	return nil
}

func roleBoundaryCode(err error) string {
	if errors.Is(err, ErrPrivateEvidence) {
		return "leak_observed"
	}
	if err != nil {
		return "unavailable"
	}
	return "observed"
}
