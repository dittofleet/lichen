// Package events is lichen's change-notification layer over crosstalk
// (github.com/dittofleet/crosstalk), which links the user's Macs through
// their own hub. lichen nudges every other connected machine whenever it
// pushes the sync repo, and each daemon listens for those nudges on the
// local crosstalk daemon's socket. crosstalk queues nothing, so a machine
// that was asleep or offline is told when the hub is back instead, and
// catches up then. A nudge only ever triggers work the hourly pass would
// do anyway: content trust comes from git, never from an event.
package events

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"syscall"
	"time"

	"lichen/internal/config"
)

// name is the crosstalk message name lichen sends and listens on.
const name = "lichen"

// socketPath is where the crosstalk daemon listens, resolved the way
// crosstalk itself does.
func socketPath() (string, error) {
	if v := os.Getenv("XDG_DATA_HOME"); v != "" {
		return filepath.Join(v, "crosstalk", "sock"), nil
	}
	return config.HomeJoin(".local", "share", "crosstalk", "sock")
}

type request struct {
	Op   string          `json:"op"`
	To   string          `json:"to,omitempty"`
	Name string          `json:"name,omitempty"`
	Body json.RawMessage `json:"body,omitempty"`
}

type response struct {
	OK      bool   `json:"ok"`
	Message string `json:"message"`
}

// line is one line a listener reads: a message from another machine, or
// a notice from the crosstalk daemon itself.
type line struct {
	From      string `json:"from"`
	Crosstalk *struct {
		Hub string `json:"hub"`
	} `json:"crosstalk"`
}

// open connects to the crosstalk daemon and sends req, returning the
// connection and a scanner positioned after the daemon's answer. A
// non-nil answer also receives that answer line. ctx's deadline, or ten
// seconds, bounds getting that answer.
func open(ctx context.Context, req request, answer any) (net.Conn, *bufio.Scanner, error) {
	sock, err := socketPath()
	if err != nil {
		return nil, nil, err
	}
	var d net.Dialer
	c, err := d.DialContext(ctx, "unix", sock)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) || errors.Is(err, syscall.ECONNREFUSED) {
			return nil, nil, errors.New("crosstalk is not running (is it installed and joined to a hub?)")
		}
		return nil, nil, err
	}
	b, _ := json.Marshal(req)
	deadline, ok := ctx.Deadline()
	if !ok {
		deadline = time.Now().Add(10 * time.Second)
	}
	c.SetDeadline(deadline)
	if _, err := c.Write(append(b, '\n')); err != nil {
		c.Close()
		return nil, nil, err
	}
	sc := bufio.NewScanner(c)
	sc.Buffer(make([]byte, 64*1024), 1024*1024)
	if !sc.Scan() {
		c.Close()
		return nil, nil, errors.New("crosstalk closed the connection without answering")
	}
	var res response
	if err := json.Unmarshal(sc.Bytes(), &res); err != nil {
		c.Close()
		return nil, nil, fmt.Errorf("crosstalk: %w", err)
	}
	if !res.OK {
		c.Close()
		return nil, nil, fmt.Errorf("crosstalk: %s", res.Message)
	}
	if answer != nil {
		json.Unmarshal(sc.Bytes(), answer)
	}
	c.SetDeadline(time.Time{})
	return c, sc, nil
}

// Announce tells every other connected machine that the sync repo moved.
// crosstalk never sends a machine its own message, so the pushing daemon
// is not nudged into a pass that has nothing to pull.
func Announce() error {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	c, _, err := open(ctx, request{Op: "send", To: "*", Name: name, Body: json.RawMessage("{}")}, nil)
	if err != nil {
		return err
	}
	return c.Close()
}

// Status describes the event channel for lichen status, and reports
// whether nudges can flow.
func Status() (string, bool) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var res struct {
		Hub     string `json:"hub"`
		Devices []struct {
			Online bool `json:"online"`
			Self   bool `json:"self"`
		} `json:"devices"`
	}
	c, _, err := open(ctx, request{Op: "devices"}, &res)
	if err != nil {
		return err.Error(), false
	}
	c.Close()
	if res.Hub != "connected" {
		return fmt.Sprintf("crosstalk is not connected to its hub (%s)", res.Hub), false
	}
	online := 0
	for _, d := range res.Devices {
		if d.Online && !d.Self {
			online++
		}
	}
	if online == 1 {
		return "crosstalk, 1 other machine connected", true
	}
	return fmt.Sprintf("crosstalk, %d other machines connected", online), true
}

// Listen follows the nudges until ctx is cancelled. caughtUp is called
// whenever messages can reach this machine again after a time they could
// not: on connecting, after the crosstalk daemon restarts, and each time
// it reconnects to the hub, as after the Mac wakes. Messages sent in
// between never arrive, so that is the moment to catch up. nudge is
// called with the machine that pushed. Both must return quickly: the
// crosstalk daemon drops a listener that stops taking lines. Listen
// connects again with capped backoff whenever the connection ends.
func Listen(ctx context.Context, onError func(error), caughtUp func(), nudge func(from string)) {
	backoff := time.Second
	for ctx.Err() == nil {
		connectedAt := time.Now()
		err := listenOnce(ctx, caughtUp, nudge)
		if ctx.Err() != nil {
			return
		}
		onError(err)
		// A connection that lasted resets the backoff, so the daemon
		// restarting for an update is picked up again within a second.
		if time.Since(connectedAt) > time.Minute {
			backoff = time.Second
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(backoff):
		}
		if backoff *= 2; backoff > time.Minute {
			backoff = time.Minute
		}
	}
}

func listenOnce(ctx context.Context, caughtUp func(), nudge func(string)) error {
	c, sc, err := open(ctx, request{Op: "listen", Name: name}, nil)
	if err != nil {
		return err
	}
	defer c.Close()
	stop := context.AfterFunc(ctx, func() { c.Close() })
	defer stop()
	for sc.Scan() {
		var l line
		if json.Unmarshal(sc.Bytes(), &l) != nil {
			continue
		}
		switch {
		case l.Crosstalk == nil:
			nudge(l.From)
		case l.Crosstalk.Hub == "connected":
			caughtUp()
		}
	}
	if err := sc.Err(); err != nil {
		return err
	}
	return errors.New("the crosstalk daemon closed the connection")
}
