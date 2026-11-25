package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/netip"
	"net/url"
	"time"
)

// healthcheckCommand is a small container probe, restricted to the local readyz
// endpoint. It deliberately bypasses proxy environment variables and redirects.
func healthcheckCommand(ctx context.Context, args []string, stderr io.Writer) int {
	fs := flag.NewFlagSet("healthcheck", flag.ContinueOnError)
	fs.SetOutput(stderr)
	target := fs.String("url", "http://127.0.0.1:8080/readyz", "literal loopback HTTP readiness URL")
	if err := fs.Parse(args); err != nil {
		if err == flag.ErrHelp {
			return 0
		}
		return 2
	}
	u, err := url.Parse(*target)
	if err != nil {
		fmt.Fprintln(stderr, "healthcheck: invalid readiness URL")
		return 2
	}
	addr, err := netip.ParseAddr(u.Hostname())
	if err != nil || !addr.IsLoopback() || u.Scheme != "http" || u.User != nil || u.Path != "/readyz" || u.RawQuery != "" || u.Fragment != "" || u.Opaque != "" || fs.NArg() != 0 {
		fmt.Fprintln(stderr, "healthcheck: use a literal loopback HTTP /readyz URL")
		return 2
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		fmt.Fprintln(stderr, "healthcheck: invalid readiness URL")
		return 2
	}
	transport := &http.Transport{DisableKeepAlives: true}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, Timeout: 5 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := client.Do(req)
	if err != nil {
		fmt.Fprintln(stderr, "healthcheck: readiness unavailable")
		return 1
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		fmt.Fprintln(stderr, "healthcheck: readiness unavailable")
		return 1
	}
	return 0
}
