package h264

import (
	"encoding/hex"
	"testing"
)

func TestStreamColor(t *testing.T) {
	// SPS from x264 at 1280x720 (ffmpeg -c:v libx264 with the colour flags
	// named).
	for name, tc := range map[string]struct {
		sps  string
		want Color
	}{
		"bt709":       {"0000016764001facd9405005bb016a02020280000003008000000301078c18cb00", Color{Described: true, Matrix: 1}},
		"unspecified": {"0000016764001facd9405005bb011000000300100000030020f183196000", Color{}},
		"bt601 full":  {"0000016764001facd9405005bb016e04040c80000003008000000301078c18cb00", Color{Described: true, Matrix: 6, FullRange: true}},
	} {
		b, _ := hex.DecodeString(tc.sps)
		got, ok := StreamColor(append(b, 0, 0, 1, 0x65, 0x88))
		if !ok || got != tc.want {
			t.Errorf("%s: got %+v (ok %v), want %+v", name, got, ok, tc.want)
		}
	}
	if _, ok := StreamColor([]byte{0, 0, 1, 0x41, 1, 2}); ok {
		t.Error("found an SPS in a P frame")
	}
}

func TestStreamColorOpenH264(t *testing.T) {
	if err := Load(); err != nil {
		t.Skip(err)
	}
	e, err := NewEncoder(320, 240, 1000, 30)
	if err != nil {
		t.Fatal(err)
	}
	defer e.Close()
	data, _, err := e.Encode(pattern(320, 240, 0), true)
	if err != nil {
		t.Fatal(err)
	}
	if c, ok := StreamColor(data); !ok || c != (Color{Described: true, Matrix: 6}) {
		t.Errorf("OpenH264 stream colour %+v (ok %v), want BT.601 limited", c, ok)
	}
}
