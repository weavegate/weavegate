package external

import (
	"io"
	"os"
	"os/exec"
	"sync"
)

// child is private: production always launches the selected owned JVM. Tests
// substitute a controllable peer without adding attach or arbitrary argv modes.
type child struct {
	stdin  io.WriteCloser
	stdout io.ReadCloser
	stderr io.ReadCloser
	wait   func() error
	kill   func() error
}

func launchJVM(java, jar string) (*child, error) {
	inR, inW, err := os.Pipe()
	if err != nil {
		return nil, errTransport
	}
	outR, outW, err := os.Pipe()
	if err != nil {
		_ = inR.Close()
		_ = inW.Close()
		return nil, errTransport
	}
	errR, errW, err := os.Pipe()
	if err != nil {
		_ = inR.Close()
		_ = inW.Close()
		_ = outR.Close()
		_ = outW.Close()
		return nil, errTransport
	}
	cmd := exec.Command(java, "-jar", jar)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = inR, outW, errW
	err = cmd.Start()
	_ = inR.Close()
	_ = outW.Close()
	_ = errW.Close()
	if err != nil {
		_ = inW.Close()
		_ = outR.Close()
		_ = errR.Close()
		return nil, errTransport
	}
	// Explicit pipes, rather than StdoutPipe, let the reader drain buffered
	// terminal/fatal/stopped frames independently of cmd.Wait reaping the child.
	return &child{stdin: inW, stdout: outR, stderr: errR, wait: cmd.Wait, kill: cmd.Process.Kill}, nil
}

// Closing owned pipes is idempotent best-effort teardown; EOF, process Wait and
// bridge joins, rather than Close return values, certify normal shutdown.
func (p *child) closePipes() {
	_ = p.stdin.Close()
	_ = p.stdout.Close()
	_ = p.stderr.Close()
}

// tail is never exported, formatted, or persisted. Keeping a bounded private
// tail drains application logs without putting secrets into public evidence.
type tail struct {
	mu    sync.Mutex
	bytes []byte
}

func (t *tail) Write(p []byte) (int, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	n := len(p)
	if n >= maxFrame {
		t.bytes = append(t.bytes[:0], p[n-maxFrame:]...)
		return n, nil
	}
	if excess := len(t.bytes) + n - maxFrame; excess > 0 {
		copy(t.bytes, t.bytes[excess:])
		t.bytes = t.bytes[:len(t.bytes)-excess]
	}
	t.bytes = append(t.bytes, p...)
	return n, nil
}
