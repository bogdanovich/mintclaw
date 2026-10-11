package config

import "testing"

func TestSteelPaidFeaturesRequireCompleteBoundedBudget(t *testing.T) {
	for _, feature := range []string{"proxy", "proxy-on-block", "captcha", "country", "partial-budget"} {
		t.Run(feature, func(t *testing.T) {
			cfg := browserSteelConfigFixture(t)
			target := cfg.Tools.Browser.Targets["cloud"]
			switch feature {
			case "proxy":
				target.Steel.UseProxy = true
			case "proxy-on-block":
				target.Steel.ProxyOnBlock = true
			case "captcha":
				target.Steel.SolveCaptcha = true
			case "country":
				target.Steel.ProxyCountry = "US"
			case "partial-budget":
				target.Steel.Budget.MaxSessionsPerDay = 1
			}
			cfg.Tools.Browser.Targets["cloud"] = target
			if err := cfg.ValidateBrowserConfig(); err == nil {
				t.Fatal("unbounded paid features admitted")
			}
		})
	}
	cfg := browserSteelConfigFixture(t)
	target := cfg.Tools.Browser.Targets["cloud"]
	target.Steel.UseProxy, target.Steel.SolveCaptcha, target.Steel.ProxyCountry = true, true, "US"
	target.Steel.Budget = BrowserSteelBudgetConfig{
		MaxSessionsPerDay: 3, MaxBillableSecondsPerDay: 900, MaxSessionCostUSD: 0.25, MaxDailyCostUSD: 0.75,
	}
	cfg.Tools.Browser.Targets["cloud"] = target
	if err := cfg.ValidateBrowserConfig(); err != nil {
		t.Fatal(err)
	}
}

func TestSteelProxyOnBlockHasOneExplicitMode(t *testing.T) {
	cfg := browserSteelConfigFixture(t)
	target := cfg.Tools.Browser.Targets["cloud"]
	target.Steel.ProxyOnBlock, target.Steel.ProxyCountry = true, "US"
	target.Steel.Budget = BrowserSteelBudgetConfig{
		MaxSessionsPerDay: 3, MaxBillableSecondsPerDay: 900, MaxSessionCostUSD: 0.25, MaxDailyCostUSD: 0.75,
	}
	cfg.Tools.Browser.Targets["cloud"] = target
	if err := cfg.ValidateBrowserConfig(); err != nil {
		t.Fatal(err)
	}
	target.Steel.UseProxy = true
	cfg.Tools.Browser.Targets["cloud"] = target
	if err := cfg.ValidateBrowserConfig(); err == nil {
		t.Fatal("simultaneous always-on and conditional proxy modes admitted")
	}
}
