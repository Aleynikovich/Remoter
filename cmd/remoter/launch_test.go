package main

import (
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
)

func TestWithoutFlags(t *testing.T) {
	got := withoutFlags([]string{"-background", "-fps", "20", "--console=true", "-password", "x", "-test-pattern"}, "background", "console")
	want := []string{"-fps", "20", "-password", "x", "-test-pattern"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestShutdownNeedsLocalRequestWithHeader(t *testing.T) {
	stopped := make(chan struct{}, 1)
	h := shutdownHandler(func() { stopped <- struct{}{} })
	for _, tc := range []struct {
		method, remote string
		header         bool
		want           int
	}{
		{http.MethodGet, "127.0.0.1:5000", true, http.StatusForbidden},
		{http.MethodPost, "127.0.0.1:5000", false, http.StatusForbidden},
		{http.MethodPost, "100.102.208.35:5000", true, http.StatusForbidden},
		{http.MethodPost, "[::1]:5000", true, http.StatusOK},
	} {
		r := httptest.NewRequest(tc.method, "/shutdown", nil)
		r.RemoteAddr = tc.remote
		if tc.header {
			r.Header.Set(stopHeader, "1")
		}
		w := httptest.NewRecorder()
		h(w, r)
		if w.Code != tc.want {
			t.Errorf("%s from %s (header %v): %d, want %d", tc.method, tc.remote, tc.header, w.Code, tc.want)
		}
	}
	<-stopped
}
