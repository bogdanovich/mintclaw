package config

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"

	"github.com/bogdanovich/mintclaw/pkg/credential"
)

type secretFieldFixture struct {
	path  string
	value string
}

func TestRepositoryDocumentsProjectEveryRegisteredSecretField(t *testing.T) {
	t.Setenv(credential.PassphraseEnvVar, "")
	t.Setenv(credential.SSHKeyPathEnvVar, "")

	cfg := DefaultConfig()
	cfg.ModelList = SecureModelList{&ModelConfig{
		ModelName: "contract-model",
		Provider:  "openai",
		Model:     "gpt-4",
		Enabled:   true,
	}}
	cfg.Channels = make(ChannelsConfig)

	fixtures := make([]secretFieldFixture, 0)
	seedSecretFields(reflect.ValueOf(cfg), "config", &fixtures)
	seedRegisteredChannelSecrets(cfg.Channels, &fixtures)
	if len(fixtures) == 0 {
		t.Fatal("secret fixture discovery found no fields")
	}

	documents, err := marshalConfigDocuments(cfg)
	if err != nil {
		t.Fatalf("marshalConfigDocuments() error = %v", err)
	}
	publicDocument := string(documents.public)
	securityDocument := string(documents.security)
	if strings.Contains(publicDocument, notHere) {
		t.Fatal("public document contains the internal secret placeholder")
	}
	for _, fixture := range fixtures {
		if strings.Contains(publicDocument, fixture.value) {
			t.Errorf("public document contains the secret for %s", fixture.path)
		}
		if !strings.Contains(securityDocument, fixture.value) {
			t.Errorf("security document omits the secret for %s", fixture.path)
		}
	}

	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.json")
	if err = os.WriteFile(configPath, documents.public, 0o600); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(securityPath(configPath), documents.security, 0o600); err != nil {
		t.Fatal(err)
	}
	loadedSnapshot, err := NewRepository(configPath).ReadDurable()
	if err != nil {
		t.Fatalf("Repository.ReadDurable() error = %v", err)
	}
	loadedFields := make(map[string][]string, len(fixtures))
	collectSecretFields(reflect.ValueOf(loadedSnapshot.Config), "config", loadedFields)
	collectRegisteredChannelSecrets(t, loadedSnapshot.Config.Channels, loadedFields)
	for _, fixture := range fixtures {
		values, ok := loadedFields[fixture.path]
		if !ok {
			t.Errorf("round trip omitted secret field %s", fixture.path)
			continue
		}
		if len(values) != 1 || values[0] != fixture.value {
			t.Errorf("round trip changed the secret bound to %s", fixture.path)
		}
		delete(loadedFields, fixture.path)
	}
	if len(loadedFields) != 0 {
		paths := make([]string, 0, len(loadedFields))
		for path := range loadedFields {
			paths = append(paths, path)
		}
		sort.Strings(paths)
		t.Fatalf("round trip produced unexpected populated secret fields: %v", paths)
	}
}

func TestRepositorySecurityDocumentRoundTripsSupportedSecretForms(t *testing.T) {
	mustSetupSSHKey(t)
	const (
		passphrase = "config-contract-passphrase"
		secret     = "config-contract-secret"
	)
	encrypted, err := credential.Encrypt(passphrase, "", secret)
	if err != nil {
		t.Fatalf("Encrypt() error = %v", err)
	}

	for _, test := range []struct {
		name       string
		raw        string
		passphrase string
	}{
		{name: "plaintext", raw: secret},
		{name: "encrypted reference", raw: encrypted, passphrase: passphrase},
		{name: "file reference", raw: "file://model.key"},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Setenv(credential.PassphraseEnvVar, test.passphrase)
			dir := t.TempDir()
			configPath := filepath.Join(dir, "config.json")
			if test.raw == "file://model.key" {
				if writeErr := os.WriteFile(
					filepath.Join(dir, "model.key"),
					[]byte(secret+"\n"),
					0o600,
				); writeErr != nil {
					t.Fatal(writeErr)
				}
			}

			cfg := DefaultConfig()
			cfg.Channels = make(ChannelsConfig)
			cfg.ModelList = SecureModelList{&ModelConfig{
				ModelName: "contract-model",
				Provider:  "openai",
				Model:     "gpt-4",
				Enabled:   true,
			}}
			documents, marshalErr := marshalConfigDocuments(cfg)
			if marshalErr != nil {
				t.Fatal(marshalErr)
			}
			securityDocument, marshalErr := yaml.Marshal(struct {
				ModelList map[string]struct {
					APIKeys []string `yaml:"api_keys"`
				} `yaml:"model_list"`
			}{
				ModelList: map[string]struct {
					APIKeys []string `yaml:"api_keys"`
				}{
					"contract-model:0": {APIKeys: []string{test.raw}},
				},
			})
			if marshalErr != nil {
				t.Fatal(marshalErr)
			}
			if writeErr := os.WriteFile(configPath, documents.public, 0o600); writeErr != nil {
				t.Fatal(writeErr)
			}
			if writeErr := os.WriteFile(securityPath(configPath), securityDocument, 0o600); writeErr != nil {
				t.Fatal(writeErr)
			}

			loadedSnapshot, loadErr := NewRepository(configPath).ReadDurable()
			if loadErr != nil {
				t.Fatalf("initial Repository.ReadDurable() error = %v", loadErr)
			}
			loaded := loadedSnapshot.Config
			if got := loaded.ModelList[0].APIKey(); got != secret {
				t.Fatal("initial resolved secret does not match")
			}
			if _, saveErr := NewRepository(configPath).Save(loaded); saveErr != nil {
				t.Fatalf("Repository.Save() error = %v", saveErr)
			}
			stored, readErr := os.ReadFile(securityPath(configPath))
			if readErr != nil {
				t.Fatal(readErr)
			}
			if !strings.Contains(string(stored), test.raw) {
				t.Fatalf("saved security document does not preserve the %s form", test.name)
			}

			reloadedSnapshot, loadErr := NewRepository(configPath).ReadDurable()
			if loadErr != nil {
				t.Fatalf("reloaded Repository.ReadDurable() error = %v", loadErr)
			}
			reloaded := reloadedSnapshot.Config
			if got := reloaded.ModelList[0].APIKey(); got != secret {
				t.Fatal("reloaded resolved secret does not match")
			}
		})
	}
}

func collectRegisteredChannelSecrets(
	t *testing.T,
	channels ChannelsConfig,
	fields map[string][]string,
) {
	t.Helper()
	for _, channel := range channels {
		if channel == nil {
			continue
		}
		settings, err := channel.GetDecoded()
		if err != nil {
			t.Fatalf("decode %s channel settings: %v", channel.Type, err)
		}
		collectSecretFields(
			reflect.ValueOf(settings),
			"channel_list."+channel.Type+".settings",
			fields,
		)
	}
}

func collectSecretFields(value reflect.Value, path string, fields map[string][]string) {
	if !value.IsValid() {
		return
	}
	for value.Kind() == reflect.Pointer {
		if value.IsNil() {
			return
		}
		value = value.Elem()
	}
	if !value.IsValid() {
		return
	}

	secureStringType := reflect.TypeOf(SecureString{})
	secureStringsType := reflect.TypeOf(SecureStrings{})
	switch value.Type() {
	case secureStringType:
		secretValue := value.Interface().(SecureString)
		if secret := secretValue.String(); secret != "" {
			fields[path] = []string{secret}
		}
		return
	case secureStringsType:
		secretValues := value.Interface().(SecureStrings)
		if secrets := (&secretValues).Values(); len(secrets) != 0 {
			fields[path] = secrets
		}
		return
	}

	switch value.Kind() {
	case reflect.Struct:
		valueType := value.Type()
		for index := 0; index < value.NumField(); index++ {
			field := valueType.Field(index)
			if field.IsExported() {
				collectSecretFields(value.Field(index), path+"."+field.Name, fields)
			}
		}
	case reflect.Slice:
		for index := 0; index < value.Len(); index++ {
			collectSecretFields(value.Index(index), fmt.Sprintf("%s[%d]", path, index), fields)
		}
	case reflect.Map:
		keys := value.MapKeys()
		sort.Slice(keys, func(i, j int) bool { return keys[i].String() < keys[j].String() })
		for _, key := range keys {
			collectSecretFields(value.MapIndex(key), path+"."+key.String(), fields)
		}
	}
}

func seedRegisteredChannelSecrets(channels ChannelsConfig, fixtures *[]secretFieldFixture) {
	channelSettingsMu.RLock()
	types := make(map[string]reflect.Type, len(channelSettingsFactory))
	for name, prototype := range channelSettingsFactory {
		types[name] = reflect.TypeOf(prototype)
	}
	channelSettingsMu.RUnlock()

	names := make([]string, 0, len(types))
	for name := range types {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		settings := reflect.New(types[name])
		fixtureCount := len(*fixtures)
		seedSecretFields(settings, "channel_list."+name+".settings", fixtures)
		if len(*fixtures) == fixtureCount {
			continue
		}
		channels["contract-"+name] = &Channel{
			Type:   name,
			extend: settings.Interface(),
		}
	}
}

func seedSecretFields(value reflect.Value, path string, fixtures *[]secretFieldFixture) {
	if !value.IsValid() {
		return
	}
	for value.Kind() == reflect.Pointer {
		if value.IsNil() {
			if !typeContainsSecret(value.Type().Elem(), make(map[reflect.Type]bool)) || !value.CanSet() {
				return
			}
			value.Set(reflect.New(value.Type().Elem()))
		}
		value = value.Elem()
	}
	if !value.IsValid() || !value.CanSet() {
		return
	}

	secureStringType := reflect.TypeOf(SecureString{})
	secureStringsType := reflect.TypeOf(SecureStrings{})
	switch value.Type() {
	case secureStringType:
		fixture := newSecretFieldFixture(path, len(*fixtures))
		value.Set(reflect.ValueOf(*NewSecureString(fixture.value)))
		*fixtures = append(*fixtures, fixture)
		return
	case secureStringsType:
		fixture := newSecretFieldFixture(path, len(*fixtures))
		value.Set(reflect.ValueOf(SimpleSecureStrings(fixture.value)))
		*fixtures = append(*fixtures, fixture)
		return
	}

	switch value.Kind() {
	case reflect.Struct:
		valueType := value.Type()
		for index := 0; index < value.NumField(); index++ {
			field := valueType.Field(index)
			if !field.IsExported() {
				continue
			}
			seedSecretFields(value.Field(index), path+"."+field.Name, fixtures)
		}
	case reflect.Slice:
		if value.Len() == 0 && typeContainsSecret(value.Type().Elem(), make(map[reflect.Type]bool)) {
			element := reflect.New(value.Type().Elem()).Elem()
			seedSecretFields(element, path+"[0]", fixtures)
			value.Set(reflect.Append(value, element))
			return
		}
		for index := 0; index < value.Len(); index++ {
			seedSecretFields(value.Index(index), fmt.Sprintf("%s[%d]", path, index), fixtures)
		}
	case reflect.Map:
		seedSecretMap(value, path, fixtures)
	}
}

func seedSecretMap(value reflect.Value, path string, fixtures *[]secretFieldFixture) {
	if value.Type().Key().Kind() != reflect.String ||
		!typeContainsSecret(value.Type().Elem(), make(map[reflect.Type]bool)) {
		return
	}
	if value.IsNil() {
		value.Set(reflect.MakeMap(value.Type()))
	}
	keys := value.MapKeys()
	if len(keys) == 0 {
		keys = []reflect.Value{reflect.ValueOf("contract").Convert(value.Type().Key())}
	}
	for _, key := range keys {
		element := reflect.New(value.Type().Elem()).Elem()
		if existing := value.MapIndex(key); existing.IsValid() {
			element.Set(existing)
		}
		seedSecretFields(element, path+"."+key.String(), fixtures)
		value.SetMapIndex(key, element)
	}
}

func typeContainsSecret(valueType reflect.Type, visiting map[reflect.Type]bool) bool {
	if valueType == reflect.TypeOf(SecureString{}) || valueType == reflect.TypeOf(SecureStrings{}) {
		return true
	}
	for valueType.Kind() == reflect.Pointer {
		valueType = valueType.Elem()
	}
	if visiting[valueType] {
		return false
	}
	visiting[valueType] = true
	defer delete(visiting, valueType)

	switch valueType.Kind() {
	case reflect.Struct:
		for index := 0; index < valueType.NumField(); index++ {
			field := valueType.Field(index)
			if field.IsExported() && typeContainsSecret(field.Type, visiting) {
				return true
			}
		}
	case reflect.Slice, reflect.Map:
		return typeContainsSecret(valueType.Elem(), visiting)
	}
	return false
}

func newSecretFieldFixture(path string, index int) secretFieldFixture {
	return secretFieldFixture{
		path:  path,
		value: fmt.Sprintf("config-secret-contract-%03d", index+1),
	}
}
