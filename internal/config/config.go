// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 Maurice Snoeren

// Package config reads the environment variables of the binaries.
//
// Every binary is configured from the environment only, because that is what
// docker compose (`environment:` and `env_file:`), `docker run -e` and an
// init script all deliver. There are no config files to mount
// and no flags to forget in a compose file.
//
// Errors are collected rather than returned one at a time, so a container that
// is missing three variables says so in one log line instead of three restarts.
package config

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

// Env reads variables and remembers what was wrong with them.
type Env struct {
	lookup func(string) (string, bool)
	errs   []string
}

// FromOS reads the process environment.
func FromOS() *Env { return &Env{lookup: os.LookupEnv} }

// FromMap reads a map; used by tests.
func FromMap(m map[string]string) *Env {
	return &Env{lookup: func(k string) (string, bool) { v, ok := m[k]; return v, ok }}
}

func (e *Env) get(name string) (string, bool) {
	v, ok := e.lookup(name)
	if !ok {
		return "", false
	}
	v = strings.TrimSpace(v)
	return v, v != ""
}

// String returns the variable, or def when it is unset or empty.
func (e *Env) String(name, def string) string {
	if v, ok := e.get(name); ok {
		return v
	}
	return def
}

// Required returns the variable and records an error when it is unset or empty.
func (e *Env) Required(name string) string {
	v, ok := e.get(name)
	if !ok {
		e.errs = append(e.errs, name+" is required")
	}
	return v
}

// Secret is Required with a minimum length. The value itself never appears in
// an error, only its name: these errors are logged.
func (e *Env) Secret(name string, minLen int) string {
	v := e.Required(name)
	if v != "" && len(v) < minLen {
		e.errs = append(e.errs, fmt.Sprintf("%s must be at least %d characters", name, minLen))
	}
	return v
}

// Duration parses a Go duration ("30s", "1m").
func (e *Env) Duration(name string, def time.Duration) time.Duration {
	v, ok := e.get(name)
	if !ok {
		return def
	}
	d, err := time.ParseDuration(v)
	if err != nil || d <= 0 {
		e.errs = append(e.errs, name+" must be a positive duration such as 30s")
		return def
	}
	return d
}

// Bool parses true/false/1/0/yes/no.
func (e *Env) Bool(name string, def bool) bool {
	v, ok := e.get(name)
	if !ok {
		return def
	}
	switch strings.ToLower(v) {
	case "1", "true", "yes", "on":
		return true
	case "0", "false", "no", "off":
		return false
	}
	b, err := strconv.ParseBool(v)
	if err != nil {
		e.errs = append(e.errs, name+" must be true or false")
		return def
	}
	return b
}

// BasePath normalises a path prefix: "" stays "", "ctf" and "/ctf/" become "/ctf".
func (e *Env) BasePath(name, def string) string {
	return NormalizeBasePath(e.String(name, def))
}

// NormalizeBasePath is the rule BasePath applies.
func NormalizeBasePath(p string) string {
	p = strings.Trim(strings.TrimSpace(p), "/")
	if p == "" {
		return ""
	}
	return "/" + p
}

// Err returns all recorded problems as one error, or nil.
func (e *Env) Err() error {
	if len(e.errs) == 0 {
		return nil
	}
	return fmt.Errorf("configuration: %s", strings.Join(e.errs, "; "))
}
