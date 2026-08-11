package main

import (
	"log"
	"net/http"

	"minimal-sfu/signaling"

	"github.com/gorilla/websocket"
)

var upgrader = websocket.Upgrader{
	// Allow all origins in development — tighten this in production
	CheckOrigin: func(r *http.Request) bool {
		return true
	},
}

func main() {
	rm := signaling.NewRoomManager()

	// WebSocket endpoint — each browser tab connects here
	http.HandleFunc("/ws", func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			log.Printf("WebSocket upgrade failed: %v", err)
			return
		}
		// Each connection gets its own goroutine
		go rm.HandleWebSocket(conn)
	})

	// Serve the browser client (index.html)
	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		http.ServeFile(w, r, "signaling/index.html")
	})

	log.Println("SFU server running on :8080")
	log.Println("Open http://localhost:8080 in multiple tabs to test")
	if err := http.ListenAndServe(":8080", nil); err != nil {
		log.Fatal("ListenAndServe: ", err)
	}
}
