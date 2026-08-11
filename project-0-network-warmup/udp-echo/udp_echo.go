package main

import (
	"fmt"
	"net"
)

func main() {

	// UDP echo server
	conn, err := net.ListenPacket("udp", ":9000")
	for {
		if err != nil {
			fmt.Println("failed to listen:", err)
			defer conn.Close()
			return
		}

		readBuf := make([]byte, 1024*2)
		n, addr, _ := conn.ReadFrom(readBuf)

		msg := string(readBuf[:n])
		fmt.Println(msg)
		fmt.Println(addr.String())

		conn.WriteTo([]byte(msg), addr)

	}
}
