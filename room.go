package main

import (
	"errors"
	"fmt"
	"io"
	"log"
	"time"

	"github.com/pion/rtcp"
	"github.com/pion/webrtc/v2"
)

const (
	rtcpPLIInterval = time.Second * 3
)

type Room struct {
	Id int32
	// api                  *webrtc.API
	peers map[int64]*Peer
	// candidates map[int64]map[string]bool
	config webrtc.Configuration
}

func NewRoom(c *Config) *Room {
	peerConnectionConfig := webrtc.Configuration{
		ICEServers: []webrtc.ICEServer{
			{
				URLs: c.Stun,
			},
		},
		SDPSemantics: webrtc.SDPSemanticsPlanB,
	}
	// m := webrtc.MediaEngine{}
	// var api = webrtc.NewAPI(webrtc.WithMediaEngine(m))
	return &Room{
		// api:                  api,
		// candidates: make(map[int64]map[string]bool),
		config: peerConnectionConfig,
		peers:  make(map[int64]*Peer),
	}
}

func (r *Room) AddPeer(uid, fromUid int64, sdp string) (answerSdp string, err error) {
	offer := webrtc.SessionDescription{
		SDP:  sdp,
		Type: webrtc.SDPTypeOffer,
	}

	// err = Decode(sdp, &offer)
	// if err != nil {
	// 	return
	// }
	var api *webrtc.API
	//发布
	if fromUid == 0 {
		m := webrtc.MediaEngine{}
		err = m.PopulateFromSDP(offer)
		if err != nil {
			return
		}
		api = webrtc.NewAPI(webrtc.WithMediaEngine(m))
	} else {
		if conn, ok := r.peers[fromUid]; ok {
			api = conn.pub.api
		}
	}
	if api == nil {
		err = errors.New("no publisher")
		return
	}

	// Create a new RTCPeerConnection
	peerConnection, err := api.NewPeerConnection(r.config)
	if err != nil {
		return
	}

	if fromUid == 0 {
		// Allow us to receive 1 video track
		if _, err = peerConnection.AddTransceiverFromKind(webrtc.RTPCodecTypeVideo); err != nil {
			_, err = peerConnection.AddTransceiverFromKind(webrtc.RTPCodecTypeAudio)
			if err != nil {
				return
			}
		}
	}

	// if _, err = peerConnection.AddTransceiverFromKind(webrtc.RTPCodecTypeAudio); err != nil {
	// 	return
	// }

	var peer, ok = r.peers[uid]
	if !ok {
		peer = NewPeer(uid)
		r.peers[uid] = peer
	}

	if fromUid != 0 {
		peer.AddReceiver(fromUid, peerConnection)
		//添加其它视频源
		for _, p := range r.peers {
			if p.pub != nil && p.pub.outputTrack != nil {
				peerConnection.AddTrack(p.pub.outputTrack)

			}
		}
	} else {
		peer.AddPublisher(api, peerConnection)
		r.OnTrack(uid, peerConnection)
	}
	r.OnIceCandidate(uid, fromUid, peer)

	peerConnection.SetRemoteDescription(offer)
	answer, err := peerConnection.CreateAnswer(nil)
	if err != nil {
		return
	}
	peerConnection.SetLocalDescription(answer)
	//TODO 将answer发送给客户端
	answerSdp = answer.SDP
	return
}

func (r *Room) AddCandidate(uid, fromUid int64, m CandiateModel) (err error) {
	var candidate = webrtc.ICECandidateInit{
		Candidate:     m.Candidate,
		SDPMLineIndex: &m.SdpMlineindex,
		SDPMid:        &m.SdpMid,
	}
	// err = json.Unmarshal([]byte(candidateJson), &candidate)
	// if err != nil {
	// 	return
	// }

	var peer, ok = r.peers[uid]
	if !ok {
		return errors.New("you are not int room")
	}
	if peer.Closed() {
		return errors.New("peer closed")
	}
	if fromUid != 0 {
		if recv, ok := peer.recvs[fromUid]; ok && !recv.Closed() {
			recv.conn.AddICECandidate(candidate)
		}

	} else {
		if !peer.pub.Closed() {
			peer.pub.conn.AddICECandidate(candidate)
		}
	}

	return
}

func (r *Room) OnTrack(uid int64, conn *webrtc.PeerConnection) {
	conn.OnTrack(func(inputTrack *webrtc.Track, receiver *webrtc.RTPReceiver) {
		go func() {
			ticker := time.NewTicker(rtcpPLIInterval)
			for range ticker.C {
				if rtcpSendErr := conn.WriteRTCP([]rtcp.Packet{&rtcp.PictureLossIndication{MediaSSRC: inputTrack.SSRC()}}); rtcpSendErr != nil {
					fmt.Println(rtcpSendErr)
				}
			}
		}()

		var outputTrack, err = conn.NewTrack(inputTrack.PayloadType(), inputTrack.SSRC(), "video", "pion")
		if err != nil {
			log.Println(err)
		}
		log.Println("add output track", uid)
		r.peers[uid].pub.outputTrack = outputTrack
		r.peers[uid].pub.conn.AddTrack(outputTrack)
		//将localTrack添加到其它reeiever
		for _, v1 := range r.peers {
			if _, ok := v1.recvs[uid]; ok {
				v1.recvs[uid].AddTrack(outputTrack)
			}
		}

		rtpBuf := make([]byte, 1400)
		for {

			// rtp, err := inputTrack.ReadRTP()
			// if err != nil {
			// 	log.Println(err)
			// 	return
			// }

			// rtp.SSRC = outputTrack.SSRC()

			// if err := outputTrack.WriteRTP(rtp); err != nil {
			// 	log.Println(err)
			// 	return
			// }
			// log.Printf("%d , send rtp: %d \n", uid, len(rtp.Payload))
			i, readErr := inputTrack.Read(rtpBuf)
			if readErr != nil {
				log.Println(err)
				return
			}

			// log.Println(uid, "recv data, len=", i)

			// ErrClosedPipe means we don't have any subscribers, this is ok if no peers have connected yet
			if _, err = outputTrack.Write(rtpBuf[:i]); err != nil && err != io.ErrClosedPipe {
				log.Println(err)
				return
			}
		}
	})
}

func (r *Room) OnIceCandidate(uid, fromUid int64, peer *Peer) {
	if fromUid != 0 {
		if recv, ok := peer.recvs[fromUid]; ok {
			recv.conn.OnICECandidate(func(c *webrtc.ICECandidate) {
				if c == nil {
					return
				}
				var candiate = c.ToJSON()
				var m = CandiateModel{
					Candidate: candiate.Candidate,
				}
				if candiate.SDPMLineIndex != nil {
					m.SdpMlineindex = *candiate.SDPMLineIndex
				}
				if candiate.SDPMid != nil {
					m.SdpMid = *candiate.SDPMid
				}
				recv.candidate = append(recv.candidate, m)
				log.Println("candidate added, uid=", uid, "fromUid=", fromUid)
			})
		}
	} else {
		peer.pub.conn.OnICECandidate(func(c *webrtc.ICECandidate) {
			if c == nil {
				return
			}
			var candiate = c.ToJSON()
			var m = CandiateModel{
				Candidate: candiate.Candidate,
			}
			if candiate.SDPMLineIndex != nil {
				m.SdpMlineindex = *candiate.SDPMLineIndex
			}
			if candiate.SDPMid != nil {
				m.SdpMid = *candiate.SDPMid
			}
			peer.pub.candidate = append(peer.pub.candidate, m)

			log.Println("candidate added, uid=", uid)
		})
	}
}

func (r *Room) GetCandidate(uid, fromUid int64) (candiate []CandiateModel, err error) {

	var peer, ok = r.peers[uid]
	if !ok {
		err = errors.New("your are not int room")
		return
	}
	if peer.Closed() {
		err = errors.New("peer closed")
		return
	}
	if fromUid != 0 {
		if recv, ok := peer.recvs[fromUid]; ok && !recv.Closed() {
			candiate = recv.candidate
		}

	} else {
		if !peer.pub.Closed() {
			candiate = peer.pub.candidate
		}

	}
	return
}

func (r *Room) ClosePeer(uid int64) {
	if p, ok := r.peers[uid]; ok {
		p.Close()
		delete(r.peers, uid)
	}
}

func (r *Room) ClosePublisher(uid int64) {
	if p, ok := r.peers[uid]; ok {
		p.ClosePublisher()
	}
}
func (r *Room) CloseReceiver(uid, fromUid int64) {
	if p, ok := r.peers[uid]; ok {
		p.CloseReceiver(fromUid)
	}
}
