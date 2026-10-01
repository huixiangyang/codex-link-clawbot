package management

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func BenchmarkRequestList(b *testing.B) {
	server, _, _ := workbenchFixture(b)
	handler := server.handler()
	r := httptest.NewRequest(http.MethodGet, "/api/requests?page_size=12", nil)
	r.Header.Set("X-Codex-Link-Token", server.token)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		if w.Code != http.StatusOK {
			b.Fatalf("request list: %d %s", w.Code, w.Body)
		}
	}
}
