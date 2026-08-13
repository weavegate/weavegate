package orchestrator

import (
	"errors"

	mysqldriver "github.com/go-sql-driver/mysql"
)

// WorkerFailureClass classifies terminal worker errors independently from
// timeout-inferred control observations.
type WorkerFailureClass string

const (
	WorkerFailureNone          WorkerFailureClass = "none"
	WorkerFailureError         WorkerFailureClass = "worker_error"
	WorkerFailureMySQLDeadlock WorkerFailureClass = "mysql_deadlock_1213"
)

// ClassifyWorkerFailure preserves MySQL deadlock 1213 through error wrapping.
// Lock-wait timeout 1205 and all other errors remain ordinary worker errors.
func ClassifyWorkerFailure(err error) WorkerFailureClass {
	if err == nil {
		return WorkerFailureNone
	}

	var mysqlErr *mysqldriver.MySQLError
	if errors.As(err, &mysqlErr) && mysqlErr.Number == 1213 {
		return WorkerFailureMySQLDeadlock
	}
	return WorkerFailureError
}
