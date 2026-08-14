package orchestrator

import (
	"errors"

	mysqldriver "github.com/go-sql-driver/mysql"

	"github.com/weavegate/weavegate/internal/trace"
)

// WorkerFailureClass classifies terminal worker errors independently from
// timeout-inferred control observations.
type WorkerFailureClass = trace.WorkerFailureClass

// All previously exported orchestrator failure-class constants remain aliases
// of the shared model during migration.
const (
	WorkerFailureNone          = trace.WorkerFailureNone
	WorkerFailureError         = trace.WorkerFailureError
	WorkerFailureMySQLDeadlock = trace.WorkerFailureMySQLDeadlock
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
