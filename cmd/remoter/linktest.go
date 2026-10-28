package main

import (
	"crypto/rand"
	"net/http"
	"strconv"
)

// Link test endpoints: viewers time /ping for latency and download
// /speedtest for throughput to this computer.
const maxSpeedtestMB = 64

func pingHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	w.Write([]byte("pong"))
}

func speedtestHandler(w http.ResponseWriter, r *http.Request) {
	mb, err := strconv.Atoi(r.URL.Query().Get("mb"))
	if err != nil || mb <= 0 {
		mb = 8
	}
	mb = min(mb, maxSpeedtestMB)
	// Random bytes, so nothing on the way can compress them.
	block := make([]byte, 64<<10)
	rand.Read(block)
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Length", strconv.Itoa(mb<<20))
	for range mb << 20 / len(block) {
		if _, err := w.Write(block); err != nil {
			return
		}
	}
}
