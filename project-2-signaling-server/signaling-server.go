package main

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"sync"

	"github.com/google/uuid"
	"github.com/gorilla/websocket"
)

var mu sync.Mutex

type Client struct {
	Id   string
	Conn *websocket.Conn
}

type Clients map[string]Client

var clients = make(Clients)

type Room struct {
	Members []string
}

var rooms = make(map[string]*Room)

type Message struct {
	Type     string `json:"type"`
	Room     string `json:"room"`
	Payload  any    `json:"payload"`
	SenderId string `json:"sender_id"`
}

var upgrader = websocket.Upgrader{
	CheckOrigin: func(r *http.Request) bool { return true },
}

func handleWS(w http.ResponseWriter, r *http.Request) {
	conn, err := upgrader.Upgrade(w, r, nil)

	clientId := uuid.New().String()
	client := Client{
		Id:   clientId,
		Conn: conn,
	}

	mu.Lock()
	clients[clientId] = client
	mu.Unlock()

	if err != nil {
		log.Println("upgrade failed: ", err)
		return
	}

	defer conn.Close()

	log.Println("new websocket connection from", conn.RemoteAddr())

	for {
		_, rawMsg, err := conn.ReadMessage()
		if err != nil {
			log.Println("read error: ", err)
			break
		}

		var msg Message
		if err := json.Unmarshal(rawMsg, &msg); err != nil {
			log.Println("bad message: ", err)
			continue
		}

		msg.SenderId = clientId

		fmt.Println("received: ", msg)

		switch msg.Type {
		case "join":
			handleJoin(clientId, msg.Room)
		case "offer", "answer", "ice-candidate":
			relayToRoom(clientId, msg)
		default:
			log.Println("unknown message type", msg.Type)
		}
	}

	log.Println("Closing connection...")
	// after the for loop
	mu.Lock()
	delete(clients, clientId)
	mu.Unlock()
	removeFromRooms(clientId)

	client.Conn.Close()
}

func handleJoin(clientId string, roomName string) {
	mu.Lock()
	defer mu.Unlock()

	room, exists := rooms[roomName]
	if !exists {
		rooms[roomName] = &Room{
			Members: []string{clientId},
		}
		log.Printf("room %s created, %s joined\n", roomName, clientId)
		return
	}

	if len(room.Members) >= 2 {
		log.Printf("room %s full, rejecting %s\n", roomName, clientId)
		// Optionally send an error back to the client
		return
	}

	room.Members = append(room.Members, clientId)
	log.Printf("%s joined room %s\n", clientId, roomName)
}

func relayToRoom(senderId string, msg Message) {
	mu.Lock()
	defer mu.Unlock()

	room, exists := rooms[msg.Room]
	if !exists {
		log.Println("Room not found ", msg.Room)
		return
	}

	for _, memberId := range room.Members {
		if memberId == senderId {
			continue
		}
		peer, ok := clients[memberId]
		if !ok {
			continue
		}
		if err := peer.Conn.WriteJSON(msg); err != nil {
			log.Println("write error to ", memberId, err)
		}
	}
}

func removeFromRooms(clientId string) {
	mu.Lock()
	defer mu.Unlock()

	for name, room := range rooms {
		for i, id := range room.Members {
			if id == clientId {
				room.Members = append(room.Members[:i], room.Members[i+1:]...)
				// Notify remaining peer
				for _, peerId := range room.Members {
					if peer, ok := clients[peerId]; ok {
						peer.Conn.WriteJSON(Message{
							Type:     "peer-left",
							Room:     name,
							SenderId: clientId,
						})
					}
				}
				if len(room.Members) == 0 {
					delete(rooms, name)
				}
				return
			}
		}
	}
}

func main() {
	http.HandleFunc("/ws", handleWS)
	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		http.ServeFile(w, r, "index.html")
	})

	log.Println("signalling server listening on: 8090")
	log.Fatal(http.ListenAndServe(":8090", nil))
}
