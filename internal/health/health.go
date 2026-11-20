// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 Maurice Snoeren

// Package health implements the -healthcheck flag of every binary.
//
// The images are FROM scratch: there is no shell, no curl and no wget to run a
// HEALTHCHECK with. So each binary checks itself: `ctf-central -healthcheck`
// GETs its own /healthz and exits 0 or 1, and the Dockerfile's HEALTHCHECK
// runs exactly that.
//
// It connects to the HOST of the listen address when there is one: a server
// bound to a network alias is not on 127.0.0.1.
package health

import (
	"fmt"
	"net"
	"net/http"
	"time"
)

// Check GETs http://<host of addr, or 127.0.0.1>:<port><path> and returns nil
// on a 2xx.
func Check(addr, path string) error {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return fmt.Errorf("listen address %q: %w", addr, err)
	}
	if host == "" || host == "0.0.0.0" || host == "::" {
		host = "127.0.0.1"
	}
	c := &http.Client{Timeout: 3 * time.Second}
	resp, err := c.Get("http://" + net.JoinHostPort(host, port) + path)
	if err != nil {
		return err
	}
	resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		return fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	return nil
}
