package main

import (
	"testing"
	"time"

	"github.com/aleynikovich/remoter/internal/protocol"
)

func TestSigninGuardDropsTypingOnSecureDesktop(t *testing.T) {
	var g signinGuard
	cases := []struct {
		in   protocol.Input
		want bool
	}{
		{protocol.Input{Kind: "keydown", Key: "a", Code: "KeyA"}, false},
		{protocol.Input{Kind: "keydown", Key: "", Code: "Digit1"}, false},
		{protocol.Input{Kind: "keydown", Key: " ", Code: "Space"}, false},
		{protocol.Input{Kind: "keydown", Key: "é"}, false},
		{protocol.Input{Kind: "keyup", Key: "a", Code: "KeyA"}, true},
		{protocol.Input{Kind: "keydown", Key: "Tab", Code: "Tab"}, true},
		{protocol.Input{Kind: "keydown", Key: "Backspace", Code: "Backspace"}, true},
		{protocol.Input{Kind: "keydown", Key: "", Code: "Delete"}, true},
		{protocol.Input{Kind: "keydown", Key: "Control", Code: "ControlLeft"}, true},
		{protocol.Input{Kind: "mousedown", Button: 0}, true},
	}
	for _, c := range cases {
		if got := g.allow("Winlogon", c.in); got != c.want {
			t.Errorf("allow(Winlogon, %+v) = %v, want %v", c.in, got, c.want)
		}
		if !g.allow("Default", c.in) {
			t.Errorf("allow(Default, %+v) = false, want true", c.in)
		}
	}
}

func TestSigninGuardLimitsEnter(t *testing.T) {
	now := time.Unix(0, 0)
	g := signinGuard{now: func() time.Time { return now }}
	enter := protocol.Input{Kind: "keydown", Key: "Enter", Code: "Enter"}
	for i := 0; i < signinEnterLimit; i++ {
		if !g.allow("Winlogon", enter) {
			t.Fatalf("Enter %d dropped, want allowed", i+1)
		}
	}
	if g.allow("Winlogon", enter) {
		t.Fatal("Enter past the limit allowed")
	}
	if !g.allow("Default", enter) {
		t.Fatal("Enter on the normal desktop dropped")
	}
	now = now.Add(signinEnterWindow)
	if !g.allow("Winlogon", enter) {
		t.Fatal("Enter after the window dropped")
	}
}
