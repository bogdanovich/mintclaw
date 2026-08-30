package cliui

import "fmt"

// PrintVersion prints stable, machine-friendly version information.
func PrintVersion(versionLine, build, goVer string) {
	fmt.Println(versionLine)
	if build != "" {
		fmt.Printf("Build: %s\n", build)
	}
	if goVer != "" {
		fmt.Printf("Go: %s\n", goVer)
	}
}
