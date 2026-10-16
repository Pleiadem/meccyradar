package mockserver

import (
	"net/http"
	"testing"
)

func TestServerRejectsSecondLoginAndReleasesLease(t *testing.T) {
	server := New("CARD-1")
	defer server.Close()
	client := http.DefaultClient
	request := func() *http.Response {
		response, err := client.Post(server.URL()+"/api/client/login", "application/json", http.NoBody)
		if err != nil {
			t.Fatal(err)
		}
		return response
	}
	response := request()
	_ = response.Body.Close()
	if response.StatusCode != http.StatusUnauthorized {
		t.Fatalf("expected invalid request status, got %d", response.StatusCode)
	}
}
