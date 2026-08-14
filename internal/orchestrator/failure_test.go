package orchestrator

import (
	"errors"
	"fmt"
	"testing"

	mysqldriver "github.com/go-sql-driver/mysql"
)

func TestClassifyWorkerFailure(t *testing.T) {
	deadlock := &mysqldriver.MySQLError{Number: 1213, Message: "deadlock found"}
	wrapped := fmt.Errorf("commit assignment: %w", deadlock)
	lockWaitTimeout := &mysqldriver.MySQLError{Number: 1205, Message: "lock wait timeout"}
	ordinary := errors.New("worker command failed")

	tests := []struct {
		name string
		err  error
		want WorkerFailureClass
	}{
		{name: "none", err: nil, want: WorkerFailureNone},
		{name: "deadlock", err: deadlock, want: WorkerFailureMySQLDeadlock},
		{name: "wrapped deadlock", err: wrapped, want: WorkerFailureMySQLDeadlock},
		{name: "lock wait timeout", err: lockWaitTimeout, want: WorkerFailureError},
		{name: "ordinary", err: ordinary, want: WorkerFailureError},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := ClassifyWorkerFailure(test.err); got != test.want {
				t.Fatalf("failure class = %q, want %q", got, test.want)
			}
		})
	}

	if ControlStatusTimeoutInferred != "timeout_inferred" {
		t.Fatalf("timeout control status = %q", ControlStatusTimeoutInferred)
	}
	if WorkerFailureMySQLDeadlock != "mysql_deadlock_1213" {
		t.Fatalf("deadlock failure class = %q", WorkerFailureMySQLDeadlock)
	}

	t.Log(
		"ORCHESTRATOR_CLASSIFY_RESULT timeout=timeout_inferred " +
			"deadlock=mysql_deadlock_1213 distinct=true wrapped=true",
	)
}
