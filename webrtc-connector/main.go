package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"time"

	"github.com/atotto/webrtc-sdp-exchanger/exchange"
	"github.com/pion/webrtc/v4"
)

var (
	sessionID      = flag.String("session", "test", "Session ID")
	commandPath    = flag.String("c", "../mibot", "execution command path")
	commandArgs    = flag.String("arg", "", "execution command arg")
	rtpPort        = flag.Int("rtpPort", 5004, "RTP Port")
	videoCodecName = flag.String("codec", "VP8", "Codec Type")
)

var config = webrtc.Configuration{
	ICEServers: []webrtc.ICEServer{
		{
			URLs: []string{
				"stun:stun.l.google.com:19302",
				"stun:stun.webrtc.ecl.ntt.com:3478",
				"stun:stun.cloudflare.com:3478",
			},
		},
	},
}

func main() {
	flag.Parse()

	ctx := context.Background()
	// TODO: cancel context

	for {
		select {
		case <-ctx.Done():
			return
		default:
		}
		if err := runSession(ctx); err != nil {
			log.Fatal(err)
		}
	}
}

func runSession(ctx context.Context) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	// Open a UDP Listener for RTP Packets on port `rtpPort`
	listener, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.ParseIP("0.0.0.0"), Port: *rtpPort})
	if err != nil {
		return fmt.Errorf(": %s", err)
	}
	defer listener.Close()

	// Increase the UDP receive buffer size
	// Default UDP buffer sizes vary on different operating systems
	if err := listener.SetReadBuffer(300 * 1024); err != nil {
		return fmt.Errorf("failed to set the buffer size: %s", err)
	}

	log.Print("getting new offer...")
	// Wait for the remote SessionDescription
	offer, err := exchange.GetSessionOffer(ctx, *sessionID)
	if err != nil {
		return fmt.Errorf("exchange session offer: %s", err)
	}

	var settingEngine = webrtc.SettingEngine{}
	settingEngine.SetNetworkTypes([]webrtc.NetworkType{webrtc.NetworkTypeUDP4})

	videoMimeType := "video/" + *videoCodecName

	mediaEngine := &webrtc.MediaEngine{}

	// Set up the codecs
	if err := mediaEngine.RegisterCodec(webrtc.RTPCodecParameters{
		RTPCodecCapability: webrtc.RTPCodecCapability{
			MimeType: videoMimeType, ClockRate: 90000, Channels: 0, SDPFmtpLine: "", RTCPFeedback: nil,
		},
	}, webrtc.RTPCodecTypeVideo); err != nil {
		return fmt.Errorf("register video codec: %s", err)
	}

	// Create a new RTCPeerConnection
	//api := webrtc.NewAPI(webrtc.WithMediaEngine(mediaEngine), webrtc.WithSettingEngine(settingEngine))
	//peerConnection, err := api.NewPeerConnection(config)
	peerConnection, err := webrtc.NewPeerConnection(config)
	if err != nil {
		return fmt.Errorf("new perr connection: %s", err)
	}

	// Create a video track
	videoTrack, err := webrtc.NewTrackLocalStaticRTP(
		webrtc.RTPCodecCapability{MimeType: videoMimeType}, "video", "pion",
	)
	if err != nil {
		return fmt.Errorf("new video track: %s", err)
	}
	rtpSender, err := peerConnection.AddTrack(videoTrack)
	if err != nil {
		return fmt.Errorf("add video track: %s", err)
	}

	// Read incoming RTCP packets
	// Before these packets are returned they are processed by interceptors. For things
	// like NACK this needs to be called.
	go func() {
		rtcpBuf := make([]byte, 1500)
		for {
			if _, _, rtcpErr := rtpSender.Read(rtcpBuf); rtcpErr != nil {
				return
			}
		}
	}()

	// Set the handler for ICE connection state
	// This will notify you when the peer has connected/disconnected
	peerConnection.OnICEConnectionStateChange(func(state webrtc.ICEConnectionState) {
		log.Printf("ICE Connection State has changed %s \n", state.String())
		if state == webrtc.ICEConnectionStateFailed || state == webrtc.ICEConnectionStateDisconnected || state == webrtc.ICEConnectionStateClosed {
			peerConnection.Close()
			cancel()
		}
	})

	peerConnection.OnConnectionStateChange(func(state webrtc.PeerConnectionState) {
		log.Printf("Connection State has changed %s \n", state.String())
		if state == webrtc.PeerConnectionStateDisconnected || state == webrtc.PeerConnectionStateClosed {
			listener.Close()
			peerConnection.Close()
			cancel()
		}
	})

	peerConnection.OnSignalingStateChange(func(state webrtc.SignalingState) {
		log.Printf("signaling State has changed %s \n", state.String())
	})

	peerConnection.OnDataChannel(func(d *webrtc.DataChannel) {
		cmd := exec.Command(*commandPath, *commandArgs)
		var w io.WriteCloser
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr

		// Register channel opening handling
		d.OnOpen(func() {
			log.Printf("Data channel '%s'-'%d' open", d.Label(), d.ID())
			w, err = cmd.StdinPipe()
			if err != nil {
				log.Printf("failed to pipe: %s", err)
				peerConnection.Close()
			}

			err := cmd.Run()
			if err != nil {
				log.Printf("failed to run command: %s", err)
				w.Close()
				peerConnection.Close()
			}
		})

		// Register text message handling
		d.OnMessage(func(msg webrtc.DataChannelMessage) {
			//log.Printf("Data Channel '%s' message: '%s'", d.Label(), string(msg.Data))
			if _, err := w.Write(msg.Data); err != nil {
				log.Printf("failed to write data: %s", err)
			}
		})

		d.OnClose(func() {
			w.Close()
			cmd.Process.Signal(syscall.SIGTERM)
			peerConnection.Close()
			listener.Close()
			log.Printf("Data channel '%s'-'%d' close", d.Label(), d.ID())
		})

		d.OnError(func(err error) {
			w.Close()
			cmd.Process.Signal(syscall.SIGTERM)
			peerConnection.Close()
			listener.Close()
			log.Printf("Data channel '%s'-'%d' error", d.Label(), d.ID(), err)
		})
	})

	// Set the remote SessionDescription
	if err = peerConnection.SetRemoteDescription(*offer); err != nil {
		return fmt.Errorf("set remote session description: %s", err)
	}

	log.Println("create answer")

	// Create answer
	answer, err := peerConnection.CreateAnswer(nil)
	if err != nil {
		return fmt.Errorf("create answer: %s", err)
	}

	// Create channel that is blocked until ICE Gathering is complete
	gatherComplete := webrtc.GatheringCompletePromise(peerConnection)

	// Sets the LocalDescription, and starts our UDP listeners
	if err = peerConnection.SetLocalDescription(answer); err != nil {
		return fmt.Errorf("set local description: %s", err)
	}

	// Block until ICE Gathering is complete, disabling trickle ICE
	// we do this because we only can exchange one signaling message
	// in a production application you should exchange ICE Candidates via OnICECandidate
	<-gatherComplete

	log.Println("send answer")

	// Send the answer
	if err := exchange.CreateSession(ctx, peerConnection.LocalDescription(), *sessionID); err != nil {
		return fmt.Errorf("send answer: %s", err)
	}

	for {
		if peerConnection.ICEConnectionState() == webrtc.ICEConnectionStateConnected {
			break
		}
		select {
		case <-time.After(time.Second):
		case <-ctx.Done():
			return nil
		}
	}

	// Read RTP packets forever and send them to the WebRTC Client
	inboundRTPPacket := make([]byte, 1600) // UDP MTU
	for {
		n, _, err := listener.ReadFrom(inboundRTPPacket)
		if err != nil {
			if strings.Contains(err.Error(), "use of closed network connection") {
				return nil
			}
			return fmt.Errorf("error during read: %s", err)
		}

		if _, err := videoTrack.Write(inboundRTPPacket[:n]); err != nil {
			if errors.Is(err, io.ErrClosedPipe) {
				return nil
			}
			return fmt.Errorf("write video: %s", err)
		}
	}

	return nil
}
