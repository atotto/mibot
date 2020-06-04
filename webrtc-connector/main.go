package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"os/exec"

	"github.com/atotto/webrtc-sdp-exchanger/exchange"
	"github.com/pion/rtp"
	"github.com/pion/webrtc/v2"
)

func main() {
	sessionID := flag.String("session", "test", "Session ID")
	commandPath := flag.String("c", "../mibot", "execution command path")
	commandArgs := flag.String("arg", "", "exacution command arg")
	flag.Parse()

	ctx := context.Background()

	// Wait for the remote SessionDescription
	offer, err := exchange.GetSessionOffer(ctx, *sessionID)
	if err != nil {
		panic(err)
	}

	config := webrtc.Configuration{
		ICEServers: []webrtc.ICEServer{
			{
				URLs: []string{"stun:stun.l.google.com:19302"},
			},
		},
	}

	settingEngine := webrtc.SettingEngine{}
	settingEngine.SetNetworkTypes([]webrtc.NetworkType{webrtc.NetworkTypeUDP4})

	// We make our own mediaEngine so we can place the sender's codecs in it.  This because we must use the
	// dynamic media type from the sender in our answer. This is not required if we are the offerer
	mediaEngine := webrtc.MediaEngine{}
	if err := mediaEngine.PopulateFromSDP(*offer); err != nil {
		panic(err)
	}

	// Search for VP8 Payload type. If the offer doesn't support VP8 exit since
	// since they won't be able to decode anything we send them
	var payloadType uint8
	for _, videoCodec := range mediaEngine.GetCodecsByKind(webrtc.RTPCodecTypeVideo) {
		if videoCodec.Name == "VP8" {
			payloadType = videoCodec.PayloadType
			break
		}
	}
	if payloadType == 0 {
		panic("Remote peer does not support VP8")
	}

	// Create a new RTCPeerConnection
	api := webrtc.NewAPI(webrtc.WithMediaEngine(mediaEngine), webrtc.WithSettingEngine(settingEngine))
	peerConnection, err := api.NewPeerConnection(config)
	if err != nil {
		panic(err)
	}

	// Open a UDP Listener for RTP Packets on port 5004
	listener, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.ParseIP("0.0.0.0"), Port: 5004})
	if err != nil {
		panic(err)
	}
	defer func() {
		if err = listener.Close(); err != nil {
			panic(err)
		}
	}()

	fmt.Println("Waiting for RTP Packets, please run GStreamer or ffmpeg now")

	// Listen for a single RTP Packet, we need this to determine the SSRC
	inboundRTPPacket := make([]byte, 4096) // UDP MTU
	n, _, err := listener.ReadFromUDP(inboundRTPPacket)
	if err != nil {
		panic(err)
	}

	// Unmarshal the incoming packet
	packet := &rtp.Packet{}
	if err = packet.Unmarshal(inboundRTPPacket[:n]); err != nil {
		panic(err)
	}

	// Create a video track, using the same SSRC as the incoming RTP Packet
	videoTrack, err := peerConnection.NewTrack(payloadType, packet.SSRC, "video", "pion")
	if err != nil {
		panic(err)
	}
	if _, err = peerConnection.AddTrack(videoTrack); err != nil {
		panic(err)
	}

	// Set the handler for ICE connection state
	// This will notify you when the peer has connected/disconnected
	peerConnection.OnICEConnectionStateChange(func(connectionState webrtc.ICEConnectionState) {
		log.Printf("Connection State has changed %s \n", connectionState.String())
	})

	peerConnection.OnDataChannel(func(d *webrtc.DataChannel) {
		log.Printf("New DataChannel %s %d\n", d.Label(), d.ID())

		ctx, cancel := context.WithCancel(ctx)
		cmd := exec.CommandContext(ctx, *commandPath, *commandArgs)
		var w io.WriteCloser

		// Register channel opening handling
		d.OnOpen(func() {
			log.Printf("Data channel '%s'-'%d' open.\n", d.Label(), d.ID())
			w, err = cmd.StdinPipe()
			if err != nil {
				log.Printf("failed to pipe: %s", err)
			}
			err := cmd.Run()
			if err != nil {
				log.Printf("failed to run command: %s", err)
			}
		})

		// Register text message handling
		d.OnMessage(func(msg webrtc.DataChannelMessage) {
			fmt.Printf("Message from DataChannel '%s': '%s'\n", d.Label(), string(msg.Data))
			if _, err := w.Write(msg.Data); err != nil {
				log.Printf("failed to write data: %s", err)
			}
		})

		d.OnClose(func() {
			w.Close()
			cancel()
		})
	})

	// Set the remote SessionDescription
	if err = peerConnection.SetRemoteDescription(*offer); err != nil {
		panic(err)
	}

	// Create answer
	answer, err := peerConnection.CreateAnswer(nil)
	if err != nil {
		panic(err)
	}

	// Sets the LocalDescription, and starts our UDP listeners
	if err = peerConnection.SetLocalDescription(answer); err != nil {
		panic(err)
	}

	// Send the answer
	if err := exchange.CreateSession(ctx, &answer, *sessionID); err != nil {
		panic(err)
	}

	// Read RTP packets forever and send them to the WebRTC Client
	for {
		n, _, err := listener.ReadFrom(inboundRTPPacket)
		if err != nil {
			fmt.Printf("error during read: %s", err)
			panic(err)
		}

		packet := &rtp.Packet{}
		if err := packet.Unmarshal(inboundRTPPacket[:n]); err != nil {
			panic(err)
		}
		packet.Header.PayloadType = payloadType

		if writeErr := videoTrack.WriteRTP(packet); writeErr != nil {
			panic(writeErr)
		}
	}
}
