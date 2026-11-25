package main

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestHealthcheckReportsReadinessWithoutLeakingResponseOrFollowingRedirects(t *testing.T) {
	for _, status := range []int{200, 403, 503, 302} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/readyz" {
					t.Error("unexpected healthcheck path")
				}
				w.Header().Set("Location", "http://example.com/secret")
				w.WriteHeader(status)
				_, _ = w.Write([]byte("private server response"))
			}))
			defer server.Close()
			var out, errb bytes.Buffer
			code := run(context.Background(), []string{"healthcheck", "--url", server.URL + "/readyz"}, strings.NewReader(""), &out, &errb, func(string) string { return "" })
			if (code == 0) != (status == 200) {
				t.Fatalf("status=%d exit=%d", status, code)
			}
			if strings.Contains(out.String()+errb.String(), "private") {
				t.Fatal("response leaked")
			}
		})
	}
}

func TestHealthcheckRejectsRemoteAndAmbiguousTargets(t *testing.T) {
	for _, url := range []string{"https://example.com/readyz", "http://localhost/readyz", "http://127.0.0.1:8080/other", "http://127.0.0.1:8080/readyz?secret=a", "http://a:b@127.0.0.1/readyz", "http://127.0.0.1/readyz#fragment"} {
		var errb bytes.Buffer
		if code := healthcheckCommand(context.Background(), []string{"--url", url}, &errb); code != 2 {
			t.Fatalf("target accepted %q: %d", url, code)
		}
	}
}
