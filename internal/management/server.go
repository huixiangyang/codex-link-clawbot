package management

import (
	"context"
	"errors"
	"net"
	"net/http"
	"time"
)

// serveHTTP 等待监听器和在途 HTTP 请求一起退出，不留下等待父 context 的后台协程。
func serveHTTP(ctx context.Context, server *http.Server, listener net.Listener) error {
	defer listener.Close()
	servingDone := make(chan struct{})
	shutdownDone := make(chan struct{})
	go func() {
		defer close(shutdownDone)
		select {
		case <-ctx.Done():
			shutdownContext, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			if err := server.Shutdown(shutdownContext); err != nil {
				_ = server.Close()
			}
		case <-servingDone:
			_ = server.Close()
		}
	}()
	err := server.Serve(listener)
	close(servingDone)
	<-shutdownDone
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}
