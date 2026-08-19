package main

import (
	"log"
	"net/http"

	"github.com/adilmayanktiwari/sfu-refactor/room"
	"github.com/adilmayanktiwari/sfu-refactor/signaling"
)

func main() {
	// 1. Create room manager
	rooms := room.NewManager()

	// 2. Create signaling handler
	sigHandler := signaling.NewHandler(rooms)

	// 3. Mount routes
	http.Handle("/ws", sigHandler)
	http.Handle("/", http.FileServer(http.Dir("signaling")))

	// 4. Start server
	addr := ":8080"
	log.Printf("[sfu] listening on %s", addr)
	log.Fatal(http.ListenAndServe(addr, nil))
}
