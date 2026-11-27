package main

import (
	"context"
	"errors"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"
)

func env(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func main() {
	addr := env("FENGLING_WS_ADDR", "127.0.0.1:9101")
	dsn := os.Getenv("FENGLING_DB_DSN")
	apiBase := env("FENGLING_API_BASE", "http://127.0.0.1:9845/api.php")
	if dsn == "" {
		log.Fatal("缺少 FENGLING_DB_DSN")
	}
	store, err := NewStore(dsn)
	if err != nil {
		log.Fatalf("连接数据库失败: %v", err)
	}
	defer store.Close()

	hub := NewHub(store, NewGateway(apiBase))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go hub.pollLoop(ctx)

	mux := http.NewServeMux()
	mux.HandleFunc("/ws", hub.ServeWS)
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true}`))
	})
	srv := &http.Server{
		Addr:              addr,
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
	}

	go func() {
		log.Printf("fengling-ws listening on %s", addr)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Fatalf("listen: %v", err)
		}
	}()

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM)
	<-stop
	log.Printf("shutting down")
	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer shutdownCancel()
	_ = srv.Shutdown(shutdownCtx)
}
