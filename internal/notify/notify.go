// Package notify puts up a desktop notification, where there is a
// desktop for one: always on macOS and Windows, and on Linux when
// notify-send is there (a server has none, and its log has to do).
package notify

func Send(msg string) {
	cmd := command(msg)
	if cmd == nil {
		return
	}
	// Not waited for: a pass is no place to sit through a notification
	// service that is slow to answer, or never does.
	if cmd.Start() == nil {
		go cmd.Wait()
	}
}
