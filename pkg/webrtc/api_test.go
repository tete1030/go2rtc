package webrtc

import (
	"fmt"
	"net"
	"testing"
	"time"

	"github.com/pion/dtls/v3"
	"github.com/pion/rtp"
	"github.com/pion/webrtc/v4"
	"github.com/stretchr/testify/require"
)

func TestSRTPReplayWindow(t *testing.T) {
	for _, profile := range []dtls.SRTPProtectionProfile{
		dtls.SRTP_AES128_CM_HMAC_SHA1_80,
		dtls.SRTP_AEAD_AES_128_GCM,
	} {
		for _, high := range []uint32{5000, 66000} {
			t.Run(fmt.Sprintf("profile_%d/high_%d", profile, high), func(t *testing.T) {
				testSRTPReplayWindow(t, profile, high)
			})
		}
	}
}

func testSRTPReplayWindow(t *testing.T, profile dtls.SRTPProtectionProfile, high uint32) {
	// Exercise the real go2rtc API, not an independently configured SRTP context.
	api, err := NewServerAPI("", "", &Filters{
		Loopback: true,
		IPs:      []string{"127.0.0.1"},
		Networks: []string{"udp4"},
	})
	require.NoError(t, err)
	receiver, err := api.NewPeerConnection(webrtc.Configuration{})
	require.NoError(t, err)
	t.Cleanup(func() { _ = receiver.Close() })

	media := &webrtc.MediaEngine{}
	require.NoError(t, media.RegisterDefaultCodecs())
	settings := webrtc.SettingEngine{}
	settings.SetIncludeLoopbackCandidate(true)
	settings.SetNetworkTypes([]webrtc.NetworkType{webrtc.NetworkTypeUDP4})
	settings.SetIPFilter(func(ip net.IP) bool { return ip.Equal(net.ParseIP("127.0.0.1")) })
	settings.SetSRTPProtectionProfiles(profile)
	// No sender NACK responder: only the packets explicitly written below may arrive.
	senderAPI := webrtc.NewAPI(webrtc.WithMediaEngine(media), webrtc.WithSettingEngine(settings))
	sender, err := senderAPI.NewPeerConnection(webrtc.Configuration{})
	require.NoError(t, err)
	t.Cleanup(func() { _ = sender.Close() })
	track, err := webrtc.NewTrackLocalStaticRTP(
		webrtc.RTPCodecCapability{MimeType: webrtc.MimeTypeH264, ClockRate: 90000},
		"video", "test",
	)
	require.NoError(t, err)
	_, err = sender.AddTrack(track)
	require.NoError(t, err)

	packets := make(chan uint16, 32)
	readError := make(chan error, 1)
	receiver.OnTrack(func(track *webrtc.TrackRemote, _ *webrtc.RTPReceiver) {
		for {
			p, _, err := track.ReadRTP()
			if err != nil {
				readError <- err
				return
			}
			packets <- p.SequenceNumber
		}
	})

	connected := make(chan struct{}, 1)
	sender.OnConnectionStateChange(func(state webrtc.PeerConnectionState) {
		if state == webrtc.PeerConnectionStateConnected {
			select {
			case connected <- struct{}{}:
			default:
			}
		}
	})
	offer, err := sender.CreateOffer(nil)
	require.NoError(t, err)
	senderGathered := webrtc.GatheringCompletePromise(sender)
	require.NoError(t, sender.SetLocalDescription(offer))
	waitSignal(t, senderGathered)
	require.NoError(t, receiver.SetRemoteDescription(*sender.LocalDescription()))
	answer, err := receiver.CreateAnswer(nil)
	require.NoError(t, err)
	receiverGathered := webrtc.GatheringCompletePromise(receiver)
	require.NoError(t, receiver.SetLocalDescription(answer))
	waitSignal(t, receiverGathered)
	require.NoError(t, sender.SetRemoteDescription(*receiver.LocalDescription()))
	waitSignal(t, connected)

	first, newest, barrier := uint16(high-2000), uint16(high), uint16(high+1)
	expected := []uint16{first, newest}
	for _, lag := range []uint32{63, 64, 147, 511, 1023} {
		expected = append(expected, uint16(high-lag))
	}
	input := append([]uint16(nil), expected...)
	// An unseen packet outside the window and replays inside it must be rejected.
	input = append(input, uint16(high-1024), uint16(high-147), newest, barrier)
	expected = append(expected, barrier)
	for _, seq := range input {
		require.NoError(t, track.WriteRTP(&rtp.Packet{
			Header:  rtp.Header{Version: 2, SequenceNumber: seq, Timestamp: 90000},
			Payload: []byte{0x41, 0x01},
		}))
	}

	// The final packet bounds the check without sleeping to infer missing packets.
	var received []uint16
	timeout := time.NewTimer(10 * time.Second)
	defer timeout.Stop()
	for {
		select {
		case seq := <-packets:
			received = append(received, seq)
			if seq == barrier {
				require.Equal(t, expected, received)
				return
			}
		case err := <-readError:
			t.Fatalf("read RTP: %v", err)
		case <-timeout.C:
			t.Fatalf("timed out waiting for RTP barrier; received %v", received)
		}
	}
}

func waitSignal(t *testing.T, done <-chan struct{}) {
	t.Helper()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("timed out establishing loopback WebRTC connection")
	}
}
