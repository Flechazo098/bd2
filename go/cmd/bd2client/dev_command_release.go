//go:build release

package main

func developmentRunOptions(args []string) ([]string, clientRunOptions, error) {
	return args, clientRunOptions{}, nil
}
