package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
)

var (
	secureStringType         = reflect.TypeOf(SecureString{})
	secureStringPointerType  = reflect.TypeOf((*SecureString)(nil))
	secureStringsType        = reflect.TypeOf(SecureStrings{})
	secureStringsPointerType = reflect.TypeOf((*SecureStrings)(nil))
	channelType              = reflect.TypeOf(Channel{})
)

// ProjectPublicConfig returns an independent configuration graph with every
// typed secret removed. Callers that persist or expose public configuration
// must serialize this projection instead of the live runtime config.
func ProjectPublicConfig(cfg *Config) (*Config, error) {
	if cfg == nil {
		return nil, errors.New("config is nil")
	}
	projected, err := projectPublicValue(reflect.ValueOf(cfg))
	if err != nil {
		return nil, err
	}
	publicCfg := projected.Interface().(*Config)
	publicCfg.sensitiveCache = nil
	return publicCfg, nil
}

func projectPublicValue(value reflect.Value) (reflect.Value, error) {
	projector := publicProjector{active: make(map[projectionReference]struct{})}
	return projector.project(value, false)
}

type projectionReference struct {
	typeOf   reflect.Type
	pointer  uintptr
	length   int
	capacity int
}

type publicProjector struct {
	active map[projectionReference]struct{}
}

func (p *publicProjector) project(value reflect.Value, rejectPrivateState bool) (reflect.Value, error) {
	if !value.IsValid() {
		return value, nil
	}
	switch value.Type() {
	case secureStringType, secureStringPointerType, secureStringsType, secureStringsPointerType:
		return reflect.Zero(value.Type()), nil
	case channelType:
		channel, err := p.projectChannel(value.Interface().(Channel))
		if err != nil {
			return reflect.Value{}, err
		}
		return reflect.ValueOf(channel), nil
	}

	switch value.Kind() {
	case reflect.Pointer:
		if value.IsNil() {
			return reflect.Zero(value.Type()), nil
		}
		leave, err := p.enter(value)
		if err != nil {
			return reflect.Value{}, err
		}
		defer leave()
		projected, err := p.project(value.Elem(), rejectPrivateState)
		if err != nil {
			return reflect.Value{}, err
		}
		result := reflect.New(value.Type().Elem())
		result.Elem().Set(projected)
		return result, nil
	case reflect.Interface:
		if value.IsNil() {
			return reflect.Zero(value.Type()), nil
		}
		projected, err := p.project(value.Elem(), true)
		if err != nil {
			return reflect.Value{}, err
		}
		result := reflect.New(value.Type()).Elem()
		result.Set(projected)
		return result, nil
	case reflect.Struct:
		result := reflect.New(value.Type()).Elem()
		for index := range value.NumField() {
			field := value.Type().Field(index)
			if !field.IsExported() {
				if rejectPrivateState && !value.Field(index).IsZero() {
					return reflect.Value{}, fmt.Errorf(
						"cannot project %s with non-zero private field %s",
						value.Type(),
						field.Name,
					)
				}
				continue
			}
			projected, err := p.project(value.Field(index), rejectPrivateState)
			if err != nil {
				return reflect.Value{}, err
			}
			result.Field(index).Set(projected)
		}
		return result, nil
	case reflect.Map:
		if value.IsNil() {
			return reflect.Zero(value.Type()), nil
		}
		leave, err := p.enter(value)
		if err != nil {
			return reflect.Value{}, err
		}
		defer leave()
		result := reflect.MakeMapWithSize(value.Type(), value.Len())
		iterator := value.MapRange()
		for iterator.Next() {
			projectedKey, err := p.project(iterator.Key(), rejectPrivateState)
			if err != nil {
				return reflect.Value{}, err
			}
			if !projectedKey.IsValid() || !projectedKey.Comparable() {
				return reflect.Value{}, fmt.Errorf("projected map key for %s is not comparable", value.Type())
			}
			if result.MapIndex(projectedKey).IsValid() {
				return reflect.Value{}, fmt.Errorf("projected map keys collide for %s", value.Type())
			}
			projected, err := p.project(iterator.Value(), rejectPrivateState)
			if err != nil {
				return reflect.Value{}, err
			}
			result.SetMapIndex(projectedKey, projected)
		}
		return result, nil
	case reflect.Slice:
		if value.IsNil() {
			return reflect.Zero(value.Type()), nil
		}
		leave, err := p.enter(value)
		if err != nil {
			return reflect.Value{}, err
		}
		defer leave()
		result := reflect.MakeSlice(value.Type(), value.Len(), value.Len())
		for index := range value.Len() {
			projected, err := p.project(value.Index(index), rejectPrivateState)
			if err != nil {
				return reflect.Value{}, err
			}
			result.Index(index).Set(projected)
		}
		return result, nil
	case reflect.Array:
		result := reflect.New(value.Type()).Elem()
		for index := range value.Len() {
			projected, err := p.project(value.Index(index), rejectPrivateState)
			if err != nil {
				return reflect.Value{}, err
			}
			result.Index(index).Set(projected)
		}
		return result, nil
	case reflect.Chan, reflect.Func, reflect.UnsafePointer:
		if rejectPrivateState && !value.IsNil() {
			return reflect.Value{}, fmt.Errorf(
				"cannot project non-zero opaque %s value of type %s",
				value.Kind(),
				value.Type(),
			)
		}
		return value, nil
	default:
		return value, nil
	}
}

func (p *publicProjector) enter(value reflect.Value) (func(), error) {
	reference := projectionReference{
		typeOf:  value.Type(),
		pointer: uintptr(value.UnsafePointer()),
	}
	if value.Kind() == reflect.Slice {
		reference.length = value.Len()
		reference.capacity = value.Cap()
	}
	if _, exists := p.active[reference]; exists {
		return nil, fmt.Errorf("cannot project cyclic %s value of type %s", value.Kind(), value.Type())
	}
	p.active[reference] = struct{}{}
	return func() { delete(p.active, reference) }, nil
}

func (p *publicProjector) projectChannel(channel Channel) (Channel, error) {
	settings, err := p.projectChannelSettings(channel)
	if err != nil {
		return Channel{}, err
	}
	groupTrigger, err := p.project(reflect.ValueOf(channel.GroupTrigger), false)
	if err != nil {
		return Channel{}, err
	}
	placeholder, err := p.project(reflect.ValueOf(channel.Placeholder), false)
	if err != nil {
		return Channel{}, err
	}
	projected := Channel{
		name:               channel.name,
		Enabled:            channel.Enabled,
		Type:               channel.Type,
		AllowFrom:          append([]string(nil), channel.AllowFrom...),
		ReasoningChannelID: channel.ReasoningChannelID,
		GroupTrigger:       groupTrigger.Interface().(GroupTriggerConfig),
		Typing:             channel.Typing,
		Placeholder:        placeholder.Interface().(PlaceholderConfig),
		Settings:           settings,
	}
	return projected, nil
}

func (p *publicProjector) projectChannelSettings(channel Channel) (RawNode, error) {
	prototype := newChannelSettings(channel.Type)
	if prototype == nil {
		return nil, fmt.Errorf("channel type %q is not registered", channel.Type)
	}

	settings := prototype
	if channel.extend != nil {
		if actual, expected := reflect.TypeOf(channel.extend), reflect.TypeOf(prototype); actual != expected {
			return nil, fmt.Errorf(
				"channel type %q settings have type %s, want %s",
				channel.Type,
				actual,
				expected,
			)
		}
		settings = channel.extend
	} else {
		if channel.Settings.IsEmpty() {
			return nil, nil
		}
		if err := channel.Settings.Decode(settings); err != nil {
			return nil, err
		}
	}
	projected, err := p.project(reflect.ValueOf(settings), false)
	if err != nil {
		return nil, err
	}
	data, err := json.Marshal(projected.Interface())
	if err != nil {
		return nil, err
	}
	return preserveExplicitDisabledStreaming(data, channel.Settings), nil
}
