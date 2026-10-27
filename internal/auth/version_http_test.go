package auth

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestVersionEndpointRejectsEmptyOrUntrustedPayload(t *testing.T) {
	for _, body := range []string{
		`{"code":200,"msg":"ok","data":null}`,
		`{"code":200,"msg":"ok","data":{}}`,
		`{"code":200,"msg":"ok"}`,
		`{"code":200,"msg":"ok","data":{"configured":false,"version":"9.9.9"}}`,
		`{"code":200,"msg":"ok","data":{"configured":true,"version":"1.0","downloadUrl":"https://pan.lanzou.com/a","notes":"","force":true}}`,
		`{"code":200,"msg":"ok","data":{"configured":true,"version":"1.1.0","downloadUrl":"https://user@example.com/a","notes":"","force":true}}`,
		`{"code":200,"msg":"ok","data":{"configured":true,"version":"1.1.0","downloadUrl":"javascript:alert(1)","notes":"","force":true}}`,
	} {
		t.Run(body, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(body)) }))
			defer server.Close()
			client, err := NewClient(server.URL, server.Client())
			if err != nil {
				t.Fatal(err)
			}
			if _, err := client.MeccyRadarVersion(context.Background()); !errors.Is(err, ErrInvalidResponse) {
				t.Fatalf("untrusted version response accepted: %v", err)
			}
		})
	}
}
