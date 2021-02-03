package format

import (
	"github.com/daneshvar/rtsp/av/avutil"
	"github.com/daneshvar/rtsp/format/mp4"
)

func RegisterAll() {
	avutil.DefaultHandlers.Add(mp4.Handler)
}
