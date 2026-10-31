package phone

import (
	"bytes"
	"context"
	"encoding/binary"
	"io"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"testing"
	"time"
)

// Expected bytes come from scrcpy's own serializer tests
// (app/tests/test_control_msg_serialize.c).

func TestTouchMsg(t *testing.T) {
	got := touchMsg(ActionDown, 100, 200, 1080, 1920)
	want := []byte{
		2, 0, // inject touch, down
		0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, // mouse pointer
		0, 0, 0, 100, 0, 0, 0, 200, 0x04, 0x38, 0x07, 0x80, // position
		0xff, 0xff, // pressure 1
		0, 0, 0, 1, // action button primary
		0, 0, 0, 1, // buttons primary
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("touch down\n got %x\nwant %x", got, want)
	}
	up := touchMsg(ActionUp, 100, 200, 1080, 1920)
	if up[1] != 1 || !bytes.Equal(up[22:], []byte{0, 0, 0, 0, 0, 1, 0, 0, 0, 0}) {
		t.Fatalf("touch up: %x", up)
	}
}

func TestScrollMsg(t *testing.T) {
	got := scrollMsg(260, 1026, 1080, 1920, 16, -16)
	want := []byte{
		3,
		0, 0, 0x01, 0x04, 0, 0, 0x04, 0x02, 0x04, 0x38, 0x07, 0x80,
		0x7f, 0xff, // 1
		0x80, 0x00, // -1
		0, 0, 0, 0,
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("\n got %x\nwant %x", got, want)
	}
}

func TestKeycodeMsg(t *testing.T) {
	got := keycodeMsg(ActionUp, 66, 5, 0x41|0x80)
	want := []byte{0, 1, 0, 0, 0, 66, 0, 0, 0, 5, 0, 0, 0, 0xc1}
	if !bytes.Equal(got, want) {
		t.Fatalf("\n got %x\nwant %x", got, want)
	}
}

func TestTextMsgsSplitOnRunes(t *testing.T) {
	text := string(bytes.Repeat([]byte("a"), 299)) + "é" + "xyz"
	msgs := textMsgs(text)
	if len(msgs) != 2 {
		t.Fatalf("%d messages", len(msgs))
	}
	var joined []byte
	for _, m := range msgs {
		n := binary.BigEndian.Uint32(m[1:])
		if m[0] != msgInjectText || int(n) != len(m)-5 || n > maxTextLen {
			t.Fatalf("bad message header %x", m[:5])
		}
		joined = append(joined, m[5:]...)
	}
	if string(joined) != text {
		t.Fatal("text changed")
	}
}

func TestSetClipboardMsg(t *testing.T) {
	got := setClipboardMsg(0x0102030405060708, "hi", true)
	want := []byte{9, 1, 2, 3, 4, 5, 6, 7, 8, 1, 0, 0, 0, 2, 'h', 'i'}
	if !bytes.Equal(got, want) {
		t.Fatalf("\n got %x\nwant %x", got, want)
	}
}

func TestKeycode(t *testing.T) {
	for _, c := range []struct {
		sc, key int
		typed   bool
	}{
		{4, 29, true}, {29, 54, true}, {30, 8, true}, {39, 7, true},
		{40, 66, false}, {42, 67, false}, {82, 19, false}, {58, 131, false}, {44, 62, true},
	} {
		key, typed := Keycode(c.sc)
		if key != c.key || typed != c.typed {
			t.Errorf("scancode %d: got %d %v, want %d %v", c.sc, key, typed, c.key, c.typed)
		}
	}
}

func TestDeviceMsgs(t *testing.T) {
	var b bytes.Buffer
	b.Write([]byte{1, 0, 0, 0, 0, 0, 0, 0, 7})              // ack
	b.Write([]byte{2, 0, 1, 0, 2, 0xaa, 0xbb})              // HID output
	b.Write([]byte{0, 0, 0, 0, 5, 'h', 'e', 'l', 'l', 'o'}) // clipboard
	for range 2 {
		ev, err := readDeviceMsg(&b)
		if err != nil || ev != nil {
			t.Fatalf("got %v %v", ev, err)
		}
	}
	ev, err := readDeviceMsg(&b)
	if err != nil || ev == nil || ev.Kind != EventClipboard || ev.Text != "hello" {
		t.Fatalf("got %+v %v", ev, err)
	}
}

// fakeServer plays scrcpy's server behind `adb forward`.
func fakeServer(t *testing.T, ln net.Listener, control chan<- []byte) {
	video, err := ln.Accept()
	if err != nil {
		return
	}
	video.Write([]byte{0})
	ctl, err := ln.Accept()
	if err != nil {
		return
	}
	t.Cleanup(func() { video.Close(); ctl.Close() })
	name := make([]byte, 64)
	copy(name, "SM-F976B")
	video.Write(name)
	video.Write([]byte("h264"))
	session := make([]byte, 12)
	session[0] = 0x80
	binary.BigEndian.PutUint32(session[4:], 904)
	binary.BigEndian.PutUint32(session[8:], 2316)
	video.Write(session)
	packet := func(flags uint64, data string) {
		h := make([]byte, 12)
		binary.BigEndian.PutUint64(h, flags)
		binary.BigEndian.PutUint32(h[8:], uint32(len(data)))
		video.Write(append(h, data...))
	}
	packet(1<<62, "SPSPPS")
	packet(1<<61|42, "IDR")
	packet(43, "P")
	ctl.Write([]byte{0, 0, 0, 0, 3, 'a', 'b', 'c'})
	buf := make([]byte, 32)
	if _, err := io.ReadFull(ctl, buf); err == nil {
		control <- buf
	}
}

func TestSession(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fake adb is a shell script")
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	control := make(chan []byte, 1)
	go fakeServer(t, ln, control)

	dir := t.TempDir()
	port := strconv.Itoa(ln.Addr().(*net.TCPAddr).Port)
	script := "#!/bin/sh\ncase \"$3\" in\n  forward) [ \"$4\" = --remove ] || echo " + port + " ;;\n  shell) sleep 5 ;;\nesac\n"
	adb := filepath.Join(dir, "adb")
	if err := os.WriteFile(adb, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	jar := filepath.Join(dir, "server")
	os.WriteFile(jar, []byte("jar"), 0o644)
	t.Setenv("REMOTER_SCRCPY_SERVER", jar)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	s, err := Start(ctx, &ADB{Path: adb}, "100.95.169.40:5555", Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if s.Name != "SM-F976B" {
		t.Errorf("name %q", s.Name)
	}
	// The clipboard arrives on its own socket, so it can come at any point
	// between the video events.
	var clip *Event
	next := func() Event {
		for {
			select {
			case ev := <-s.Events():
				if ev.Kind == EventClipboard {
					clip = &ev
					continue
				}
				return ev
			case <-ctx.Done():
				t.Fatal("no event")
			}
			return Event{}
		}
	}
	if ev := next(); ev.Kind != EventSize || ev.Width != 904 || ev.Height != 2316 {
		t.Fatalf("size event %+v", ev)
	}
	if ev := next(); ev.Kind != EventFrame || string(ev.Data) != "SPSPPSIDR" || !ev.Key {
		t.Fatalf("first frame %+v", ev)
	}
	if f := next(); f.Kind != EventFrame || string(f.Data) != "P" || f.Key {
		t.Errorf("second frame %+v", f)
	}
	for clip == nil {
		select {
		case ev := <-s.Events():
			if ev.Kind == EventClipboard {
				clip = &ev
			}
		case <-ctx.Done():
			t.Fatal("no clipboard event")
		}
	}
	if clip.Text != "abc" {
		t.Errorf("clipboard %+v", *clip)
	}
	s.Touch(ActionDown, 0.5, 1)
	msg := <-control
	if want := touchMsg(ActionDown, 452, 2315, 904, 2316); !bytes.Equal(msg, want) {
		t.Errorf("touch\n got %x\nwant %x", msg, want)
	}
}

func TestMessageTypes(t *testing.T) {
	// scrcpy numbers them by their order in enum sc_control_msg_type.
	for got, want := range map[int]int{msgBackOrScreenOn: 4, msgSetClipboard: 9, msgSetDisplayPower: 10, msgRotateDevice: 11, msgResetVideo: 17} {
		if got != want {
			t.Errorf("type %d, want %d", got, want)
		}
	}
}
