## StateChange

1. offer = pc.CreateOffer()
2. pc.SetLocalDescription(offer)
3. OnICEGatheringStateChange: gathering
4. OnSignalingStateChange: have-local-offer
*. OnICEGatheringStateChange: complete
5. pc.SetRemoteDescription(answer)
6. OnSignalingStateChange: stable
7. OnICEConnectionStateChange: checking
8. OnICEConnectionStateChange: connected

## SRTP replay window and NACK repair

The WebRTC API uses a 1024-packet SRTP replay-protection window. Pion's default
64-packet window can reject a previously unseen repair packet after later
packets have advanced the receive index. This can defeat NACK recovery even
though authentication keys and the repaired media are valid.

The configured window covers the current default 512-packet NACK receive
history with headroom. It remains finite: packets outside the window and
duplicates inside it are still rejected. SRTCP settings are unchanged.
This setting neither disables authentication nor orders the delivered RTP.

The mismatch is discussed in
[Pion webrtc-v3-design #36](https://github.com/pion/webrtc-v3-design/issues/36).
In a controlled WebRTC-to-RTSP investigation, a repair arriving 147 sequence
positions late was authenticated and forwarded after increasing this window.
That observation supports this receive-window change, not claims about every
source of packet loss or long-term stream stability.

`TestSRTPReplayWindow` exercises **NewServerAPI itself** with two loopback-only
WebRTC peers, using AES-CM/HMAC-SHA1 and AES-GCM. It checks previously unseen
packets at lags 63, 64, 147, 511 and 1023, rejects an unseen packet at lag 1024
and true duplicates, and repeats across the RTP sequence-number rollover.
It fails against the unmodified API configuration.

```sh
go test -race ./pkg/webrtc
go test -race ./pkg/webrtc -run TestSRTPReplayWindow -count=20
```

No custom H.264 guard, jitter buffer, depacketizer, packet rewriting, runtime
probe, or device-specific configuration is included. Local downstream H.264
recovery policies used in that investigation are separate from this fix.
