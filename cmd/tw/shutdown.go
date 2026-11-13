package main

import (
	"context"
	"log"
	"net/http"
	"time"
)

// Cleanup can block in a transport or a child process. It must not keep the
// console listener and the terminal alive indefinitely after Ctrl+C.
func shutdownConsole(srv *http.Server, grace time.Duration, cleanup func()) {
	ctx, cancel := context.WithTimeout(context.Background(), grace)
	defer cancel()
	done := make(chan struct{})
	go func() {
		defer close(done)
		cleanup()
	}()
	select {
	case <-done:
	case <-ctx.Done():
		log.Printf("shutdown: 清理超时，关闭控制台；再次启动时检查宿主恢复状态")
	}
	if err := srv.Shutdown(ctx); err != nil {
		_ = srv.Close()
	}
}
