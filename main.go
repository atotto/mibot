package main

import (
	"bufio"
	"context"
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/go-ble/ble"
	"github.com/go-ble/ble/examples/lib/dev"
)

var (
	microbitName    = flag.String("m", "", "microbit name")
	control         = flag.String("control", "stdin", "control option (stdin|udp)")
	controlDuration = flag.Duration("control.period", 200*time.Millisecond, "period for continuing control of the command")
)

var (
	UART_SERVICE_UUID = ble.MustParse(`6E400001-B5A3-F393-E0A9-E50E24DCCA9E`)
	TX_CHAR_UUID      = ble.MustParse(`6E400002-B5A3-F393-E0A9-E50E24DCCA9E`)
	RX_CHAR_UUID      = ble.MustParse(`6E400003-B5A3-F393-E0A9-E50E24DCCA9E`)
)

func main() {
	flag.Parse()

	var input io.Reader
	s := *control
	switch {
	case s == "stdin":
		input = os.Stdin
	case strings.HasPrefix(s, "udp"):
		conn, err := optUDPConn(s)
		if err != nil {
			log.Fatal(err)
		}
		input = conn
		defer conn.Close()
	default:
		log.Fatal("invalid control option: %s", s)
	}

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGTERM, syscall.SIGINT)
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		<-sig
		cancel()
	}()

	device, err := dev.DefaultDevice()
	if err != nil {
		log.Fatal(err)
	}

	ble.SetDefaultDevice(device)

	go func() {
		log.Println("connecting...")

		client, err := ble.Connect(ctx, func(a ble.Advertisement) bool {
			if a.Connectable() && strings.HasPrefix(a.LocalName(), "BBC micro:bit") && strings.Contains(a.LocalName(), *microbitName) {
				log.Printf("connect to %s", a.LocalName())
				return true
			}
			return false
		})
		if err != nil {
			log.Fatalf("failed to connect: %s", err)
		}
		go func() {
			<-client.Disconnected()
			cancel()
		}()

		p, err := client.DiscoverProfile(true)
		if err != nil {
			log.Fatalf("failed to discover profile: %s", err)
		}

		c := p.FindCharacteristic(ble.NewCharacteristic(RX_CHAR_UUID))

		reader := bufio.NewReader(input)
		fmt.Println("send commands (w:forward a:left d:right s:backward enter:send ctrl-c:exit)")

		for {
			b, _ := reader.ReadByte()
			if err := client.WriteCharacteristic(c, []byte{b, 0x0a}, true); err != nil {
				log.Println("send data: %s", err)
			}

			select {
			case <-ctx.Done():
				return
			case <-client.Disconnected():
				return
			case <-time.After(*controlDuration):
			}
		}
	}()

	<-ctx.Done()

	log.Println("done")
}
