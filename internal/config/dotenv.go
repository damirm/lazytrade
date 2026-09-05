package config

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"regexp"
	"strconv"
	"strings"
)

var environmentNamePattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// LoadDotEnv loads KEY=VALUE entries without overwriting variables already
// present in the process environment. A missing file is intentionally ignored.
func LoadDotEnv(path string) error {
	file, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("open dotenv %q: %w", path, err)
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	for lineNumber := 1; scanner.Scan(); lineNumber++ {
		name, value, skip, err := parseDotEnvLine(scanner.Text())
		if err != nil {
			return fmt.Errorf("dotenv %q line %d: %w", path, lineNumber, err)
		}
		if skip {
			continue
		}
		if _, exists := os.LookupEnv(name); exists {
			continue
		}
		if err := os.Setenv(name, value); err != nil {
			return fmt.Errorf("dotenv %q line %d: %w", path, lineNumber, err)
		}
	}
	if err := scanner.Err(); err != nil {
		return fmt.Errorf("read dotenv %q: %w", path, err)
	}
	return nil
}

func parseDotEnvLine(line string) (name, value string, skip bool, err error) {
	line = strings.TrimSpace(line)
	if line == "" || strings.HasPrefix(line, "#") {
		return "", "", true, nil
	}
	line = strings.TrimSpace(strings.TrimPrefix(line, "export "))
	name, raw, found := strings.Cut(line, "=")
	name = strings.TrimSpace(name)
	if !found || !environmentNamePattern.MatchString(name) {
		return "", "", false, errors.New("expected a valid KEY=VALUE entry")
	}
	raw = strings.TrimSpace(raw)
	if len(raw) >= 2 && (raw[0] == '"' || raw[0] == '\'') {
		if raw[len(raw)-1] != raw[0] {
			return "", "", false, errors.New("unterminated quoted value")
		}
		if raw[0] == '"' {
			decoded, decodeErr := strconv.Unquote(raw)
			if decodeErr != nil {
				return "", "", false, fmt.Errorf("decode quoted value: %w", decodeErr)
			}
			raw = decoded
		} else {
			raw = raw[1 : len(raw)-1]
		}
	}
	return name, raw, false, nil
}
