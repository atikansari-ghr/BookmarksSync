package main

import (
	"errors"
	"strings"
)

func extractServeEnvironmentFile(args []string) ([]string, string, error) {
	remaining := make([]string, 0, len(args))
	path := ""
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if arg == "--" {
			remaining = append(remaining, args[i:]...)
			break
		}
		value, hasValue := strings.CutPrefix(arg, "--env-file=")
		if arg == "--env-file" {
			if i+1 >= len(args) || args[i+1] == "" {
				return nil, "", errors.New("--env-file requires a path")
			}
			i++
			value, hasValue = args[i], true
		}
		if hasValue {
			if value == "" {
				return nil, "", errors.New("--env-file requires a path")
			}
			if path != "" {
				return nil, "", errors.New("--env-file may be specified only once")
			}
			path = value
			continue
		}
		remaining = append(remaining, arg)
	}
	return remaining, path, nil
}
