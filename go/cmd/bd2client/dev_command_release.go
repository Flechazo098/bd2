//go:build release

package main

func relaunchDevelopmentIfNeeded([]string) (bool, error) { return false, nil }

func developmentRunOptions(args []string) ([]string, clientRunOptions, error) {
	return args, clientRunOptions{}, nil
}
