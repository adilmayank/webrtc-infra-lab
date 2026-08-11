package main

import (
	"crypto/rand"
	"encoding/binary"
	"fmt"
	"net"
	"time"
)

func main() {

	//	dialing up google's stun server
	conn, err := net.Dial("udp4", "stun.l.google.com:19302")

	if err != nil {
		fmt.Println("Couldn't connect with public stun server.")
		conn.Close()

		return
	}

	bindingRequest := buildBindingRequest()
	publicIPString, err := getMyPublicIP(conn, bindingRequest)

	fmt.Println(publicIPString)

	defer conn.Close()

}

func buildBindingRequest() []byte {
	buf := make([]byte, 20)
	binary.BigEndian.PutUint16(buf[0:2], 0x0001)
	binary.BigEndian.PutUint16(buf[2:4], 0x0000)
	binary.BigEndian.PutUint32(buf[4:8], 0x2112A442)
	rand.Read(buf[8:20])

	return buf
}

func getMyPublicIP(conn net.Conn, bindingRequest []byte) (string, error) {
	conn.Write(bindingRequest)
	conn.SetDeadline(time.Now().Add(time.Second * 3))

	response := make([]byte, 1024)
	_, err := conn.Read(response)

	if err != nil {
		return "", fmt.Errorf("Couldn't read the response")
	}

	fmt.Println(response)

	//	PORT
	xorPort := binary.BigEndian.Uint16(response[26:28])
	port := xorPort ^ 0x2112

	//	IP
	xorIP := binary.BigEndian.Uint32(response[28:32])
	ip := xorIP ^ 0x2112A442

	ipString := net.IPv4(byte(ip>>24), byte(ip>>16), byte(ip>>8), byte(ip))
	ipPortString := fmt.Sprintf("%s:%d", ipString.String(), port)

	return ipPortString, nil
}
