//go:build release

package main

func runDevelopmentCommand([]string) (bool, error) { return false, nil }

func developmentUsage() string { return "" }
