package main

import (
	"flag"
	"log"
	"net/http"
	"time"

	"screen_server/internal/rtc"
	"screen_server/internal/signaling"
)

func main() {
	addr := flag.String("addr", ":8080", "HTTP listen address")
	staticDir := flag.String("static", "./frontend/dist", "directory for built frontend assets")
	flag.Parse()

	rtcManager, err := rtc.NewManager()
	if err != nil {
		log.Fatalf("create rtc manager: %v", err)
	}

	hub, err := signaling.NewHub(rtcManager)
	if err != nil {
		log.Fatalf("create signaling hub: %v", err)
	}
	go hub.Run()

	mux := http.NewServeMux()
	mux.HandleFunc("/api/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true}`))
	})
	mux.HandleFunc("/api/signaling/ws", hub.ServeWS)

	// In development the React app is usually served by Vite on :5173.
	// For production, run `npm run build` in frontend/ and this serves dist/.
	mux.Handle("/", http.FileServer(http.Dir(*staticDir)))

	server := &http.Server{
		Addr:              *addr,
		Handler:           logRequests(mux),
		ReadHeaderTimeout: 5 * time.Second,
	}

	log.Printf("screen server listening on %s", *addr)
	if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Fatalf("server failed: %v", err)
	}
}

func logRequests(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		next.ServeHTTP(w, r)
		log.Printf("%s %s %s", r.Method, r.URL.Path, time.Since(start).Round(time.Millisecond))
	})
}
