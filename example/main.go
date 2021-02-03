package main

import (
	"context"
	"fmt"
	"io"
	"net"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/daneshvar/go-log"
	"github.com/daneshvar/rtsp/av"
	"github.com/daneshvar/rtsp/client"
	"github.com/daneshvar/rtsp/codec"
	"github.com/daneshvar/rtsp/codec/h264parser"
)

const timeOut = 20e9

type readResult struct {
	pkt *av.Packet
	err error
}

func main() {
	if len(os.Args) < 2 {
		log.Fatal("First argument must be a RTSP URI")
	}

	uri := os.Args[1]
	conn, err := client.DialTimeout(uri, timeOut)
	if err != nil {
		log.Fatalv("Open Connection", "uri", uri, "error", err)
	}
	defer conn.Close()

	ctx, cancel := context.WithTimeout(context.Background(), timeOut)
	defer cancel()

	// Read RTSP Streams of Audio, Video, Metadata, ...
	streams, err := getStream(ctx, conn)
	if err != nil {
		log.Fatalv("Read Streams", "uri", uri, "error", err)
	}

	sps, pps, timeScale, err := getH264Stream(streams)
	if err != nil {
		log.Fatalv("Get H264 Stream", "uri", uri, "error", err)
	}

	done := make(chan struct{})
	wait := make(chan struct{})
	go func() {
		defer close(wait)
		loop(done, conn, sps, pps, timeScale)
	}()

	termChan := make(chan os.Signal)
	signal.Notify(termChan, syscall.SIGINT, syscall.SIGTERM)

	select {
	case <-termChan:
		close(done)
		log.Info("Waiting to close loop")
		select {
		case <-wait:
		case <-time.After(timeOut):
			log.Info("Timeout to close loop")
		}
	case <-wait:
		log.Info("closed loop")
	}
}

func getStream(ctx context.Context, conn *client.Client) ([]client.Stream, error) {
	resChan := make(chan []client.Stream, 1)
	errChan := make(chan error, 1)
	go func() {
		if streams, err := conn.Streams(); err != nil {
			errChan <- err
		} else {
			resChan <- streams
		}
	}()

	select {
	case streams := <-resChan:
		return streams, nil
	case err := <-errChan:
		return nil, err
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func getH264Stream(streams []client.Stream) (sps []byte, pps []byte, timeScale int, err error) {
	for i := range streams {
		stream := &streams[i]
		codecData := stream.CodecData

		switch codecData.Type() {
		case av.H264:
			if h264, ok := codecData.(h264parser.CodecData); ok {
				log.Infov("H264 Codec", "AVType", stream.Sdp.AVType, "Rtpmap", stream.Sdp.Rtpmap, "TimeScale", stream.Sdp.TimeScale)
				sps = h264.SPS()
				pps = h264.PPS()
				timeScale = stream.Sdp.TimeScale
				return
			} else {
				log.Errorv("H264 CodecData", "type", codecData.Type().String(), "error", err)
				err = fmt.Errorf("H264 Codec can't cast to H264 CodecData")
				return
			}
		case av.PCM_MULAW, av.PCM_ALAW, av.AAC:
			if audio, ok := codecData.(av.AudioCodecData); ok {
				log.Infov("Audio Codec", "type", codecData.Type().String(), "rate", audio.SampleRate())
			} else {
				log.Errorv("Audio CodecData", "type", codecData.Type().String(), "error", err)
			}
		case av.ONVIF_METADATA:
			metadata := codecData.(codec.MetadataData)
			log.Infov("codec", "type", codecData.Type().String(), "uri", metadata.URI())
		default:
			log.Warnv("Codec is not implement", "type", codecData.Type().String())
		}
	}

	err = fmt.Errorf("H264 Codec not found")
	return
}

func loop(done <-chan struct{}, conn *client.Client, sps []byte, pps []byte, timeScale int) {
	log.Debugv("Start Loop with Values", "SPS", len(sps), "PPS", len(pps), "TimeScale", timeScale)

	result := make(chan readResult, 1)
	for {
		go func() {
			pkt, err := conn.ReadPacket()
			result <- readResult{&pkt, err}
		}()

		select {
		case r := <-result:
			if proc(r) {
				return
			}
		case <-time.After(timeOut):
			log.Error("Read Packet Timeout")
			return
		case <-done:
			log.Debug("Connection Loop closing")
			return
		}
	}
}

func proc(r readResult) (fail bool) {
	if r.err != nil {
		if r.err == io.EOF {
			log.Warn("Read Packet EOF")
			return true
		}

		if netErr, ok := r.err.(net.Error); ok && netErr.Timeout() {
			log.Errorv("Read Packet Timeout Error", "error", r.err)
			return true
		}

		log.Errorv("Read Packet", "error", r.err)
		return strings.Contains(r.err.Error(), "use of closed network connection")
	}

	if len(r.pkt.Data) == 0 {
		log.Warn("Read Packet, Data len is zero")
		return false
	}

	log.Infov("Ready Packet", "len", len(r.pkt.Data), "KeyFrame", r.pkt.IsKeyFrame, "IsAudio", r.pkt.IsAudio, "IsMetadata", r.pkt.IsMetadata)

	return false
}
