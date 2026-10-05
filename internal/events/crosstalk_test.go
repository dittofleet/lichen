package events

import (
	"bufio"
	"context"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"
)

// fakeDaemon stands in for the crosstalk daemon's socket: each connection
// is handed to serve with the request it opened with.
func fakeDaemon(t *testing.T, serve func(c net.Conn, req request)) {
	t.Helper()
	// Unix socket paths are short on macOS, so not t.TempDir.
	dir, err := os.MkdirTemp("", "lichen")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	t.Setenv("XDG_DATA_HOME", dir)
	os.MkdirAll(filepath.Join(dir, "crosstalk"), 0o700)
	ln, err := net.Listen("unix", filepath.Join(dir, "crosstalk", "sock"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				defer c.Close()
				sc := bufio.NewScanner(c)
				if !sc.Scan() {
					return
				}
				var req request
				json.Unmarshal(sc.Bytes(), &req)
				serve(c, req)
			}()
		}
	}()
}

func writeLine(c net.Conn, s string) { c.Write([]byte(s + "\n")) }

func TestAnnounce(t *testing.T) {
	got := make(chan request, 1)
	fakeDaemon(t, func(c net.Conn, req request) {
		got <- req
		writeLine(c, `{"ok":true,"sent":["kiwi"]}`)
	})
	if err := Announce(); err != nil {
		t.Fatal(err)
	}
	req := <-got
	if req.Op != "send" || req.To != "*" || req.Name != name {
		t.Fatalf("request = %+v, want a send to every machine on %q", req, name)
	}
}

func TestAnnounceRefused(t *testing.T) {
	fakeDaemon(t, func(c net.Conn, req request) {
		writeLine(c, `{"error":"offline","message":"this machine is not connected to the hub"}`)
	})
	if err := Announce(); err == nil {
		t.Fatal("want an error when crosstalk refuses")
	}
}

func TestAnnounceNotRunning(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	if err := Announce(); err == nil {
		t.Fatal("want an error with no crosstalk daemon")
	}
}

// Listen tells nudges from catch-up notices, and connects again when the
// daemon closes the connection, as it does on restarting.
func TestListen(t *testing.T) {
	var conns atomic.Int32
	fakeDaemon(t, func(c net.Conn, req request) {
		if req.Op != "listen" || req.Name != name {
			t.Errorf("request = %+v, want a listen on %q", req, name)
			return
		}
		n := conns.Add(1)
		writeLine(c, `{"ok":true}`)
		writeLine(c, `{"crosstalk":{"hub":"connected"}}`)
		if n == 1 {
			writeLine(c, `{"id":"1","from":"kiwi","name":"lichen","body":{},"wantsReply":false}`)
			return // the daemon restarting
		}
		time.Sleep(time.Minute)
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	seen := make(chan string, 10)
	done := make(chan struct{})
	go func() {
		Listen(ctx,
			func(error) { seen <- "error" },
			func() { seen <- "caught up" },
			func(from string) { seen <- "nudge from " + from })
		close(done)
	}()
	want := []string{"caught up", "nudge from kiwi", "error", "caught up"}
	for _, w := range want {
		select {
		case got := <-seen:
			if got != w {
				t.Fatalf("got %q, want %q", got, w)
			}
		case <-time.After(5 * time.Second):
			t.Fatalf("timed out waiting for %q", w)
		}
	}
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Listen did not return once cancelled")
	}
}
