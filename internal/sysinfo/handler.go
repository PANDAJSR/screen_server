package sysinfo

import (
	"encoding/json"
	"net/http"
)

func HandleSessions(w http.ResponseWriter, r *http.Request) {
	sessions, err := EnumSessions()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if sessions == nil {
		sessions = []SessionInfo{}
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(sessions)
}

func HandleDisplays(w http.ResponseWriter, r *http.Request) {
	displays, err := EnumDisplays()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if displays == nil {
		displays = []DisplayInfo{}
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(displays)
}

func HandleWindows(w http.ResponseWriter, r *http.Request) {
	wins, err := EnumWindows_()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if wins == nil {
		wins = []WindowInfo{}
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(wins)
}
