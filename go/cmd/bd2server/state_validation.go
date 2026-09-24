package main

import (
	"fmt"
	"strings"

	"bd2server/internal/accountstate"
)

func stateProblemsError(prefix string, problems []accountstate.Problem) error {
	parts := make([]string, 0, len(problems))
	for _, problem := range problems {
		parts = append(parts, problem.Code+": "+problem.Message)
	}
	return fmt.Errorf("%s: %s", prefix, strings.Join(parts, "; "))
}
