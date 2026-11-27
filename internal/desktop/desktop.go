// Package desktop talks to the desktop session.
package desktop

import "os/exec"

// Notify shows a desktop notification from germ, if notify-send is
// installed, and waits for notify-send to exit. body may be empty.
func Notify(title, body string) {
	p, err := exec.LookPath("notify-send")
	if err != nil {
		return
	}
	args := []string{"--app-name=germ", title}
	if body != "" {
		args = append(args, body)
	}
	_ = exec.Command(p, args...).Run()
}
