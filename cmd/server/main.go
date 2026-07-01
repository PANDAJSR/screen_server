package main

import (
	"encoding/json"
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
	"screen_server/internal/sysinfo"

	"github.com/pion/webrtc/v4"
)

// serverConfig mirrors the JSON config file.
type serverConfig struct {
	ICEServers []rtc.ICEServerConfig `json:"iceServers"`
}

func main() {
	addr := flag.String("addr", ":8080", "HTTP listen address")
	staticDir := flag.String("static", "./frontend/dist", "directory for built frontend assets")
	logDir := flag.String("logdir", "./logs", "directory for log files")
	configPath := flag.String("config", "./server-config.json", "path to server config JSON")
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

	// ---- Load ICE server config ----
	iceServers := loadICEServers(*configPath)

	rtcManager, err := rtc.NewManager(iceServers)
	if err != nil {
		log.Fatalf("create rtc manager: %v", err)
	}

	// Marshal the public ICE config (includes credentials for the signaling
	// channel) so the hub can relay it to browsers on join.
	iceConfigJSON, err := json.Marshal(rtcManager.GetICEServers())
	if err != nil {
		log.Fatalf("marshal ice config: %v", err)
	}

	hub, err := signaling.NewHub(rtcManager, iceConfigJSON)
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
	mux.HandleFunc("/api/sessions", sysinfo.HandleSessions)
	mux.HandleFunc("/api/displays", sysinfo.HandleDisplays)
	mux.HandleFunc("/api/windows", sysinfo.HandleWindows)

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

// loadICEServers reads the config file and converts to pion webrtc.ICEServer.
// Falls back to a public STUN-only config if the file is missing or invalid.
func loadICEServers(path string) []webrtc.ICEServer {
	defaultServers := []webrtc.ICEServer{
		{URLs: []string{"stun:stun.l.google.com:19302"}},
	}

	data, err := os.ReadFile(path)
	if err != nil {
		log.Printf("config file %s not found, using default STUN-only ICE config: %v", path, err)
		return defaultServers
	}

	var cfg serverConfig
	if err := json.Unmarshal(data, &cfg); err != nil {
		log.Printf("config file %s parse error, using default STUN-only: %v", path, err)
		return defaultServers
	}

	if len(cfg.ICEServers) == 0 {
		log.Printf("config file %s has no iceServers, using default STUN-only", path)
		return defaultServers
	}

	servers := make([]webrtc.ICEServer, 0, len(cfg.ICEServers))
	for _, s := range cfg.ICEServers {
		servers = append(servers, webrtc.ICEServer{
			URLs:       s.URLs,
			Username:   s.Username,
			Credential: s.Credential,
		})
	}

	var urls []string
	for _, s := range servers {
		urls = append(urls, s.URLs...)
	}
	log.Printf("loaded ICE servers from %s: %v", path, urls)
	return servers
}

func logRequests(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		next.ServeHTTP(w, r)
		log.Printf("%s %s %s", r.Method, r.URL.Path, time.Since(start).Round(time.Millisecond))
	})
}
