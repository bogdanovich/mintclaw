package cliui

import "fmt"

// PrintOnboardComplete prints the post-onboard ready message and next steps.
func PrintOnboardComplete(encrypt bool, configPath string) {
	fmt.Println("\nMintClaw is ready!")
	fmt.Println("\nNext steps:")
	if encrypt {
		fmt.Println("  1. Set your encryption passphrase before starting MintClaw:")
		fmt.Println("       export MINTCLAW_KEY_PASSPHRASE=<your-passphrase>   # Linux/macOS")
		fmt.Println("       set MINTCLAW_KEY_PASSPHRASE=<your-passphrase>      # Windows cmd")
		fmt.Println()
		fmt.Println("  2. Add your API key to", configPath)
	} else {
		fmt.Println("  1. Add your API key to", configPath)
	}
	fmt.Println("\n     Recommended:")
	fmt.Println("     - OpenRouter: https://openrouter.ai/keys (access 100+ models)")
	fmt.Println("     - Ollama:     https://ollama.com (local, free)")
	fmt.Println("\n     See README.md for supported providers.")
	if encrypt {
		fmt.Println("\n  3. Chat: mintclaw agent -m \"Hello!\"")
	} else {
		fmt.Println("\n  2. Chat: mintclaw agent -m \"Hello!\"")
	}
}
