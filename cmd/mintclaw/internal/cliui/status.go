package cliui

import "fmt"

// ProviderRow holds one provider's display name and status value.
type ProviderRow struct {
	Name string
	Val  string
}

// StatusReport is a structured status view for PrintStatus.
type StatusReport struct {
	Version       string
	Build         string
	ConfigPath    string
	ConfigOK      bool
	WorkspacePath string
	WorkspaceOK   bool
	Model         string
	Providers     []ProviderRow
	OAuthLines    []string
}

// PrintStatus renders terminal-neutral status information.
func PrintStatus(r StatusReport) {
	fmt.Println("MintClaw status")
	fmt.Printf("Version: %s\n", r.Version)
	if r.Build != "" {
		fmt.Printf("Build: %s\n", r.Build)
	}
	fmt.Println()

	printPathLine("Config", r.ConfigPath, r.ConfigOK)
	printPathLine("Workspace", r.WorkspacePath, r.WorkspaceOK)

	if r.ConfigOK {
		fmt.Printf("Model: %s\n", r.Model)
		for _, p := range r.Providers {
			fmt.Printf("%s: %s\n", p.Name, p.Val)
		}
		if len(r.OAuthLines) > 0 {
			fmt.Println("\nOAuth/Token Auth:")
			for _, line := range r.OAuthLines {
				fmt.Printf("  %s\n", line)
			}
		}
	}
}

func printPathLine(label, path string, ok bool) {
	mark := "missing"
	if ok {
		mark = "ok"
	}
	fmt.Printf("%s: %s (%s)\n", label, path, mark)
}
