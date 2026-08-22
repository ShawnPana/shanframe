package main

// Hidden `shanframe _screencap out.h264 [seconds]`: dump raw Annex-B H.264
// from the native capture for eyeballing with ffplay/ffprobe.

import (
	"fmt"
	"os"
	"strconv"
	"time"

	"github.com/shawnpana/shanframe/internal/screencap"
)

func screencapDebug(args []string) error {
	if !screencap.Supported() {
		return fmt.Errorf("not supported on this platform")
	}
	if len(args) < 1 {
		return fmt.Errorf("usage: shanframe _screencap out.h264 [seconds]")
	}
	secs := 3
	if len(args) > 1 {
		secs, _ = strconv.Atoi(args[1])
	}
	if !screencap.Authorized() {
		fmt.Println("requesting Screen Recording permission…")
		screencap.RequestPermission()
	}
	f, err := os.Create(args[0])
	if err != nil {
		return err
	}
	defer f.Close()
	frames, keys, bytes := 0, 0, 0
	s, err := screencap.Start(1920, 30, 6_000_000, func(fr screencap.Frame) {
		f.Write(fr.Data)
		frames++
		bytes += len(fr.Data)
		if fr.Key {
			keys++
		}
	})
	if err != nil {
		return err
	}
	fmt.Printf("capturing %dx%d for %ds…\n", s.W, s.H, secs)
	time.Sleep(time.Duration(secs) * time.Second)
	s.Stop()
	fmt.Printf("%d frames (%d key), %.1f MB\n", frames, keys, float64(bytes)/1e6)
	return nil
}
