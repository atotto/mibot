package main

import (
	"context"
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
	"github.com/pion/rtp"
	"github.com/pion/webrtc/v2"
)

var (
	sessionID      = flag.String("session", "test", "Session ID")
	commandPath    = flag.String("c", "../mibot", "execution command path")
	commandArgs    = flag.String("arg", "", "exacution command arg")
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
	//listener, err := net.ListenPacket("udp", fmt.Sprintf("%s:%d", "127.0.0.1", *rtpPort))
	listener, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.ParseIP("0.0.0.0"), Port: *rtpPort})
	if err != nil {
		return fmt.Errorf(": %s", err)
	}
	defer listener.Close()

	log.Printf("Waiting for RTP Packets on port %s, please run GStreamer or ffmpeg now", listener.LocalAddr().String())

	// Listen for a single RTP Packet, we need this to determine the SSRC
	inboundRTPPacket := make([]byte, 4096) // UDP MTU
	n, _, err := listener.ReadFromUDP(inboundRTPPacket)
	if err != nil {
		return fmt.Errorf("read RTP packets: %s", err)
	}

	// Unmarshal the incoming packet
	packet := &rtp.Packet{}
	if err = packet.Unmarshal(inboundRTPPacket[:n]); err != nil {
		return fmt.Errorf("unmarshal RTP packet: %s", err)
	}

	log.Print("getting new offer...")
	// Wait for the remote SessionDescription
	offer, err := exchange.GetSessionOffer(ctx, *sessionID)
	if err != nil {
		return fmt.Errorf("exchange session offer: %s", err)
	}

	var settingEngine = webrtc.SettingEngine{}
	settingEngine.SetNetworkTypes([]webrtc.NetworkType{webrtc.NetworkTypeUDP4})

	// We make our own mediaEngine so we can place the sender's codecs in it.  This because we must use the
	// dynamic media type from the sender in our answer. This is not required if we are the offerer
	mediaEngine := webrtc.MediaEngine{}
	if err := mediaEngine.PopulateFromSDP(*offer); err != nil {
		return fmt.Errorf("SDP: %s", err)
	}

	// Search for `videoCodecName` Payload type. If the offer doesn't support specified codec exit
	// since they won't be able to decode anything we send them
	var payloadType uint8
	for _, videoCodec := range mediaEngine.GetCodecsByKind(webrtc.RTPCodecTypeVideo) {
		if videoCodec.Name == *videoCodecName {
			payloadType = videoCodec.PayloadType
			break
		}
	}
	if payloadType == 0 {
		log.Print("Remote peer does not support VP8")
		return nil // retry
	}

	// Create a new RTCPeerConnection
	api := webrtc.NewAPI(webrtc.WithMediaEngine(mediaEngine), webrtc.WithSettingEngine(settingEngine))
	peerConnection, err := api.NewPeerConnection(config)
	if err != nil {
		return fmt.Errorf("new perr connection: %s", err)
	}

	// Create a video track, using the same SSRC as the incoming RTP Packet
	videoTrack, err := peerConnection.NewTrack(payloadType, packet.SSRC, "video", "pion")
	if err != nil {
		return fmt.Errorf("new video track: %s", err)
	}
	if _, err = peerConnection.AddTrack(videoTrack); err != nil {
		return fmt.Errorf("add video track: %s", err)
	}

	// Set the handler for ICE connection state
	// This will notify you when the peer has connected/disconnected
	peerConnection.OnICEConnectionStateChange(func(state webrtc.ICEConnectionState) {
		log.Printf("ICE Connection State has changed %s \n", state.String())
		if state == webrtc.ICEConnectionStateFailed || state == webrtc.ICEConnectionStateDisconnected || state == webrtc.ICEConnectionStateClosed {
			listener.Close()
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

	// Create answer
	answer, err := peerConnection.CreateAnswer(nil)
	if err != nil {
		return fmt.Errorf("create answer: %s", err)
	}

	// Sets the LocalDescription, and starts our UDP listeners
	if err = peerConnection.SetLocalDescription(answer); err != nil {
		return fmt.Errorf("set local description: %s", err)
	}

	// Send the answer
	if err := exchange.CreateSession(ctx, &answer, *sessionID); err != nil {
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
	for {
		n, _, err := listener.ReadFrom(inboundRTPPacket)
		if err != nil {
			if strings.Contains(err.Error(), "use of closed network connection") {
				return nil
			}
			return fmt.Errorf("error during read: %s", err)
		}

		packet := &rtp.Packet{}
		if err := packet.Unmarshal(inboundRTPPacket[:n]); err != nil {
			return fmt.Errorf("unmarshal RTP packet: %s", err)
		}
		packet.Header.PayloadType = payloadType

		if err := videoTrack.WriteRTP(packet); err != nil {
			if err == io.ErrClosedPipe {
				return nil
			}
			return fmt.Errorf("write video: %s", err)
		}
	}

	return nil
}
