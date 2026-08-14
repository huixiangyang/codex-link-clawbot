package bridge

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestUploadToCDNUsesOfficialDownloadHeader(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		response.Header().Set("X-Encrypted-Param", "download-token")
		response.Header().Set("X-Encrypted-Query-Param", "legacy-token")
		response.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	got, err := uploadToCDN(context.Background(), []byte("encrypted"), server.URL)
	if err != nil {
		t.Fatal(err)
	}
	if got != "download-token" {
		t.Fatalf("download token = %q", got)
	}
}

func TestUploadToCDNRejectsMissingOfficialDownloadHeader(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		response.Header().Set("X-Encrypted-Query-Param", "wrong-token")
		response.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	if _, err := uploadToCDN(context.Background(), []byte("encrypted"), server.URL); err == nil {
		t.Fatal("missing X-Encrypted-Param should fail")
	}
}
