package fixture

import (
	"errors"
	"fmt"
	"strings"
	"sync"
)

// ErrConnectionDescriptorInvalid reports that a fixture-owned connection
// descriptor is not available for a new application connection.
var ErrConnectionDescriptorInvalid = errors.New("fixture connection descriptor is invalid")

// ConnectionDescriptor identifies the prepared fixture's application
// database. Its password is deliberately kept out of exported fields so
// generic formatting and serialization cannot disclose it.
//
// A descriptor is valid after Provision and across successful Reset calls.
// Teardown invalidates every copy before cleanup starts. Callers may copy the
// value, but must not retain the password returned by Password beyond the
// owned, private transport write that consumes it.
type ConnectionDescriptor struct {
	Driver   string
	Host     string
	Port     int
	Name     string
	Username string

	secret *connectionSecret
}

type connectionSecret struct {
	mu       sync.RWMutex
	password string
	valid    bool
}

func newConnectionDescriptor(
	driver string,
	host string,
	port int,
	name string,
	username string,
	password string,
) ConnectionDescriptor {
	return ConnectionDescriptor{
		Driver:   driver,
		Host:     host,
		Port:     port,
		Name:     name,
		Username: username,
		secret: &connectionSecret{
			password: password,
			valid:    true,
		},
	}
}

// Password returns the application password while the prepared fixture owns
// a live descriptor. It never returns a password after teardown invalidation.
func (d ConnectionDescriptor) Password() (string, error) {
	if d.secret == nil {
		return "", ErrConnectionDescriptorInvalid
	}

	d.secret.mu.RLock()
	defer d.secret.mu.RUnlock()
	if !d.secret.valid {
		return "", ErrConnectionDescriptorInvalid
	}

	return d.secret.password, nil
}

// Valid reports whether Password may authorize a connection to the fixture.
func (d ConnectionDescriptor) Valid() bool {
	if d.secret == nil {
		return false
	}

	d.secret.mu.RLock()
	defer d.secret.mu.RUnlock()
	return d.secret.valid
}

func (d ConnectionDescriptor) invalidate() {
	if d.secret == nil {
		return
	}

	d.secret.mu.Lock()
	d.secret.password = ""
	d.secret.valid = false
	d.secret.mu.Unlock()
}

// String redacts the application password by construction.
func (d ConnectionDescriptor) String() string {
	return fmt.Sprintf(
		"ConnectionDescriptor{Driver:%q Host:%q Port:%d Name:%q Username:%q Password:<redacted> Valid:%t}",
		d.Driver,
		d.Host,
		d.Port,
		d.Name,
		d.Username,
		d.Valid(),
	)
}

// GoString redacts the application password for %#v formatting too.
func (d ConnectionDescriptor) GoString() string { return d.String() }

type redactedConnectionError struct {
	message string
	cause   error
}

func (e *redactedConnectionError) Error() string { return e.message }

func (e *redactedConnectionError) Is(target error) bool {
	return errors.Is(e.cause, target)
}

// redactConnectionError strips credentials from returned text without
// retaining them in an unwrap-accessible error chain. Is still preserves
// sentinel and context error classification.
func redactConnectionError(err error, secrets ...string) error {
	if err == nil {
		return nil
	}

	message := err.Error()
	for _, secret := range secrets {
		if secret != "" {
			message = strings.ReplaceAll(message, secret, "<redacted>")
		}
	}
	return &redactedConnectionError{message: message, cause: err}
}
