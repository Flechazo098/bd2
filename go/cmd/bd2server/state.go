package main

import (
	"errors"
	"flag"
	"fmt"
	"path/filepath"

	"bd2server/internal/accountstate"
)

func stateCommand(args []string) error {
	if len(args) == 0 || args[0] != "check" {
		return errors.New("usage: bd2server state check [options]")
	}
	return stateCheckCommand(args[1:])
}

func stateCheckCommand(args []string) error {
	fs := flag.NewFlagSet("state check", flag.ContinueOnError)
	stateDB := fs.String("state", filepath.FromSlash("../data/state/state.db"), "account SQLite database")
	if err := fs.Parse(args); err != nil {
		return err
	}
	repository, err := accountstate.Open(filepath.Clean(*stateDB))
	if err != nil {
		return err
	}
	defer repository.Close()
	version, err := repository.SchemaVersion()
	if err != nil {
		return err
	}
	problems, err := repository.Validate()
	if err != nil {
		return err
	}
	fmt.Printf("schema version: %d\n", version)
	for _, problem := range problems {
		fmt.Printf("ERROR %s %s: %s", problem.Code, filepath.Join(problem.Path...), problem.Message)
		if len(problem.RelatedIDs) != 0 {
			fmt.Printf(" ids=%v", problem.RelatedIDs)
		}
		fmt.Println()
	}
	if len(problems) != 0 {
		return fmt.Errorf("state check rejected with %d error(s)", len(problems))
	}
	fmt.Println("state check passed")
	return nil
}
