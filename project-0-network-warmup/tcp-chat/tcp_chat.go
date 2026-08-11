package main

import (
	"bufio"
	"fmt"
	"net"
)

type Message struct {
	data   []byte
	sender net.Conn
}

func main() {

	register := make(chan net.Conn)
	unregister := make(chan net.Conn)
	broadcast := make(chan Message)

	go func() {
		clients := make(map[net.Conn]bool)
		for {
			select {
			case conn := <-register:
				clients[conn] = true
			case conn := <-unregister:
				delete(clients, conn)
				conn.Close()
			case msg := <-broadcast:
				for c := range clients {
					if c != msg.sender {
						c.Write(msg.data)
					}
				}
			}
		}
	}()

	listener, _ := net.Listen("tcp", ":9000")
	for {
		fmt.Println("For loop entered. About to call accept for new conection...")
		conn, _ := listener.Accept()
		fmt.Println("New connection created...")
		fmt.Println("Handling new connection...")
		go handleConnection(conn, register, unregister, broadcast)
	}
}

func handleConnection(conn net.Conn, register, unregister chan net.Conn, broadcast chan Message) {
	fmt.Println("Pulling connection into register channel...")
	register <- conn

	scanner := bufio.NewScanner(conn)
	fmt.Println("Waiting for new message to come...")
	for scanner.Scan() {
		message := Message{
			data:   []byte(scanner.Text() + "\n"),
			sender: conn,
		}
		fmt.Println("Received some message over tcp connection. Broadcasting it to others...")
		broadcast <- message
	}
	fmt.Println("Closing connection...")
	unregister <- conn
}
