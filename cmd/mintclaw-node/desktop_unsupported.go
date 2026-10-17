//go:build !darwin

package main

func defaultDesktopArgs() ([]string, error) { return nil, nil }
