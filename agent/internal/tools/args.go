package tools

import (
	"encoding/json"
	"fmt"
	"math"
	"strconv"
	"strings"
)

// Args are the arguments of one tool call.
type Args map[string]any

// parseArgs decodes the raw JSON arguments; no arguments is an empty set.
func parseArgs(raw json.RawMessage) (Args, error) {
	args := Args{}
	if len(raw) == 0 || string(raw) == "null" {
		return args, nil
	}
	if err := json.Unmarshal(raw, &args); err != nil {
		return nil, fmt.Errorf("the arguments are not a JSON object: %w", err)
	}
	return args, nil
}

// Int returns the named integer, def when it is absent. Whole numbers and numeric strings are accepted.
func (a Args) Int(name string, def int) (int, error) {
	v, ok := a[name]
	if !ok || v == nil {
		return def, nil
	}
	switch n := v.(type) {
	case float64:
		if n != math.Trunc(n) || math.Abs(n) > 1e9 {
			return 0, fmt.Errorf("%s must be a whole number, got %v", name, n)
		}
		return int(n), nil
	case string:
		i, err := strconv.Atoi(strings.TrimSpace(n))
		if err != nil {
			return 0, fmt.Errorf("%s must be a whole number, got %q", name, n)
		}
		return i, nil
	}
	return 0, fmt.Errorf("%s must be a whole number", name)
}

// RequireInt is Int for a parameter without a default.
func (a Args) RequireInt(name string) (int, error) {
	if _, ok := a[name]; !ok {
		return 0, fmt.Errorf("missing required parameter %s", name)
	}
	return a.Int(name, 0)
}

// Float returns the named number, def when it is absent.
func (a Args) Float(name string, def float64) (float64, error) {
	v, ok := a[name]
	if !ok || v == nil {
		return def, nil
	}
	switch n := v.(type) {
	case float64:
		return n, nil
	case string:
		f, err := strconv.ParseFloat(strings.TrimSpace(n), 64)
		if err != nil {
			return 0, fmt.Errorf("%s must be a number, got %q", name, n)
		}
		return f, nil
	}
	return 0, fmt.Errorf("%s must be a number", name)
}

// Bool returns the named flag; the strings "true", "1" and "yes" count as true, like the Python server.
func (a Args) Bool(name string, def bool) bool {
	v, ok := a[name]
	if !ok || v == nil {
		return def
	}
	switch b := v.(type) {
	case bool:
		return b
	case string:
		switch strings.ToLower(b) {
		case "true", "1", "yes":
			return true
		}
		return false
	}
	return def
}

// String returns the named string, def when it is absent.
func (a Args) String(name, def string) string {
	if s, ok := a[name].(string); ok {
		return s
	}
	return def
}

// RequireString is String for a parameter without a default.
func (a Args) RequireString(name string) (string, error) {
	s, ok := a[name].(string)
	if !ok {
		return "", fmt.Errorf("missing required parameter %s", name)
	}
	return s, nil
}
