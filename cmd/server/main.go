package main

import (
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"screen_server/internal/rtc"
	"screen_server/internal/signaling"
)

func main() {
	addr := flag.String("addr", ":8080", "HTTP listen address")
	staticDir := flag.String("static", "./frontend/dist", "directory for built frontend assets")
	logDir := flag.String("logdir", "./logs", "directory for log files")
	flag.Parse()

	// Ensure log directory exists.
	if err := os.MkdirAll(*logDir, 0755); err != nil {
		log.Fatalf("create log dir: %v", err)
	}

	// Open timestamped log file and tee output to both file and stderr.
	logName := filepath.Join(*logDir, time.Now().Format("2006-01-02_150405")+".log")
	logFile, err := os.Create(logName)
	if err != nil {
		log.Fatalf("create log file: %v", err)
	}
	defer logFile.Close()

	multi := io.MultiWriter(os.Stderr, logFile)
	log.SetOutput(multi)
	log.SetFlags(log.LstdFlags | log.Lmicroseconds | log.Lmsgprefix)
	log.SetPrefix("")

	fmt.Fprintf(os.Stderr, "logging to %s\n", logName)

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
