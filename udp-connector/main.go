package main

import (
	"bufio"
	"log"
	"net"
	"os"
)

func main() {
	conn, err := net.Dial("udp", "127.0.0.1:8823")
	if err != nil {
		log.Fatal(err)
	}
	defer conn.Close()

	reader := bufio.NewReader(os.Stdin)

	for {
		b, err := reader.ReadByte()
		if err != nil {
			log.Fatal(err)
		}
		n, err := conn.Write([]byte{b})
		if err != nil {
			log.Fatal(err)
		}
		if n != 1 {
			log.Printf("sent data size is %d", n)
		}
	}
}
