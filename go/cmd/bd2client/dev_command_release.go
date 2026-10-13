//go:build release

package main

func relaunchDevelopmentIfNeeded([]string) (bool, error) { return false, nil }

func runClientDevelopmentCommand([]string) (bool, error) { return false, nil }
