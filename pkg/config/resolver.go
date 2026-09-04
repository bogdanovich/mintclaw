package config

import (
	"fmt"
	"reflect"
	"strings"

	"github.com/bogdanovich/mintclaw/pkg/credential"
)

func resolveConfigSecrets(cfg *Config, resolver *credential.Resolver) error {
	if cfg == nil {
		return fmt.Errorf("config is nil")
	}
	if resolver == nil {
		return fmt.Errorf("credential resolver is nil")
	}
	return resolveSecureValues(reflect.ValueOf(cfg), resolver)
}

func resolveSecureValues(value reflect.Value, resolver *credential.Resolver) error {
	if !value.IsValid() {
		return nil
	}

	switch value.Type() {
	case secureStringType:
		secret := value.Interface().(SecureString)
		if err := resolveSecureString(&secret, resolver); err != nil {
			return err
		}
		value.Set(reflect.ValueOf(secret))
		return nil
	case secureStringPointerType:
		if value.IsNil() {
			return nil
		}
		return resolveSecureString(value.Interface().(*SecureString), resolver)
	case channelType:
		return resolveChannelSecrets(value.Addr().Interface().(*Channel), resolver)
	}

	switch value.Kind() {
	case reflect.Pointer:
		if value.IsNil() {
			return nil
		}
		return resolveSecureValues(value.Elem(), resolver)
	case reflect.Interface:
		if value.IsNil() {
			return nil
		}
		resolved := reflect.New(value.Elem().Type()).Elem()
		resolved.Set(value.Elem())
		if err := resolveSecureValues(resolved, resolver); err != nil {
			return err
		}
		value.Set(resolved)
		return nil
	case reflect.Struct:
		for index := range value.NumField() {
			if !value.Type().Field(index).IsExported() {
				continue
			}
			if err := resolveSecureValues(value.Field(index), resolver); err != nil {
				return err
			}
		}
	case reflect.Map:
		if value.IsNil() {
			return nil
		}
		iterator := value.MapRange()
		for iterator.Next() {
			resolved := reflect.New(value.Type().Elem()).Elem()
			resolved.Set(iterator.Value())
			if err := resolveSecureValues(resolved, resolver); err != nil {
				return err
			}
			value.SetMapIndex(iterator.Key(), resolved)
		}
	case reflect.Slice, reflect.Array:
		for index := range value.Len() {
			if err := resolveSecureValues(value.Index(index), resolver); err != nil {
				return err
			}
		}
	}
	return nil
}

func resolveSecureString(secret *SecureString, resolver *credential.Resolver) error {
	if secret == nil {
		return nil
	}
	raw := secret.raw
	if raw == "" && hasCredentialReferencePrefix(secret.resolved) {
		raw = secret.resolved
		secret.raw = raw
	}
	if raw == "" {
		return nil
	}
	resolved, err := resolver.Resolve(raw)
	if err != nil {
		return err
	}
	secret.resolved = resolved
	return nil
}

func hasCredentialReferencePrefix(value string) bool {
	return strings.HasPrefix(value, credential.EncScheme) || strings.HasPrefix(value, credential.FileScheme)
}

func resolveChannelSecrets(channel *Channel, resolver *credential.Resolver) error {
	settings, err := channel.GetDecoded()
	if err != nil {
		return err
	}
	if settings == nil {
		return nil
	}
	return resolveSecureValues(reflect.ValueOf(settings), resolver)
}
