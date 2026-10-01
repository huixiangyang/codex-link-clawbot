package management

import (
	"context"
	"io"
	"net"
	"net/http"
	"testing"
	"time"
)

func TestHTTPShutdownWaitsForActiveRequest(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	entered := make(chan struct{})
	release := make(chan struct{}, 1)
	defer close(release)
	shutdown := make(chan struct{})
	server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(entered)
		// 模拟必须完成响应的管理操作，不因服务停止就丢弃在途请求。
		select {
		case <-release:
		case <-r.Context().Done():
		}
		_, _ = w.Write([]byte("finished"))
	})}
	server.RegisterOnShutdown(func() { close(shutdown) })
	done := make(chan error, 1)
	go func() { done <- serveHTTP(ctx, server, listener) }()
	responseDone := make(chan error, 1)
	go func() {
		client := &http.Client{Timeout: 3 * time.Second}
		response, err := client.Get("http://" + listener.Addr().String())
		if err == nil {
			_, err = io.Copy(io.Discard, response.Body)
			_ = response.Body.Close()
		}
		responseDone <- err
	}()
	select {
	case <-entered:
	case <-ctx.Done():
		t.Fatal("request did not start")
	}
	cancel()
	select {
	case <-shutdown:
	case <-time.After(time.Second):
		t.Fatal("shutdown did not start")
	}
	select {
	case err := <-done:
		t.Fatalf("server returned while a request was active: %v", err)
	case <-time.After(30 * time.Millisecond):
	}
	release <- struct{}{}
	select {
	case err := <-responseDone:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("request did not finish")
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("server did not finish shutdown")
	}
}
