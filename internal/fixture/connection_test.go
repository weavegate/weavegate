package fixture

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
)

func TestConnectionDescriptorRedactsAndInvalidatesCopies(t *testing.T) {
	t.Parallel()

	const password = "application-secret"
	descriptor := newConnectionDescriptor(
		"mysql",
		"127.0.0.1",
		3306,
		"weavegate",
		"weavegate",
		password,
	)
	copyOfDescriptor := descriptor

	gotPassword, err := copyOfDescriptor.Password()
	if err != nil {
		t.Fatalf("read descriptor password: %v", err)
	}
	if gotPassword != password {
		t.Fatalf("descriptor password = %q, want supplied secret", gotPassword)
	}

	for _, formatted := range []string{
		descriptor.String(),
		fmt.Sprintf("%v", descriptor),
		fmt.Sprintf("%+v", descriptor),
		fmt.Sprintf("%#v", descriptor),
		fmt.Sprintf("%+v", DB{Connection: descriptor}),
		fmt.Sprintf("%#v", DB{Connection: descriptor}),
	} {
		if strings.Contains(formatted, password) {
			t.Fatalf("formatted descriptor disclosed password: %q", formatted)
		}
		if !strings.Contains(formatted, "<redacted>") {
			t.Fatalf("formatted descriptor omitted redaction marker: %q", formatted)
		}
	}
	encoded, err := json.Marshal(descriptor)
	if err != nil {
		t.Fatalf("marshal descriptor metadata: %v", err)
	}
	if strings.Contains(string(encoded), password) {
		t.Fatalf("serialized descriptor disclosed password: %s", encoded)
	}

	descriptor.invalidate()
	if descriptor.Valid() || copyOfDescriptor.Valid() {
		t.Fatal("descriptor copy remained valid after fixture invalidation")
	}
	if password, err := copyOfDescriptor.Password(); !errors.Is(err, ErrConnectionDescriptorInvalid) || password != "" {
		t.Fatalf("invalid descriptor password = %q, error = %v", password, err)
	}
}

func TestConnectionErrorRedactionPreservesClassification(t *testing.T) {
	t.Parallel()

	wantErr := errors.New("connection failed")
	err := redactConnectionError(
		fmt.Errorf("%w: user-secret and admin-secret", wantErr),
		"user-secret",
		"admin-secret",
	)
	if strings.Contains(err.Error(), "user-secret") || strings.Contains(err.Error(), "admin-secret") {
		t.Fatalf("redacted error disclosed a credential: %v", err)
	}
	if !strings.Contains(err.Error(), "<redacted>") {
		t.Fatalf("redacted error = %q, want marker", err)
	}
	if !errors.Is(err, wantErr) {
		t.Fatalf("redacted error does not preserve errors.Is classification: %v", err)
	}
	if unwrapped := errors.Unwrap(err); unwrapped != nil {
		t.Fatalf("redacted error exposes underlying credential-bearing error: %v", unwrapped)
	}
}
