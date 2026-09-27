package config

func defaultConfigWithTestModel() *Config {
	cfg := DefaultConfig()
	cfg.ModelList = SecureModelList{&ModelConfig{
		ModelName: "test-model",
		Provider:  "openai",
		Model:     "test-model",
		Enabled:   true,
	}}
	return cfg
}
