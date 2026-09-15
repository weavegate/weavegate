package matchingsut

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/weavegate/weavegate/internal/sut/gonative"
)

type handler struct {
	service   *service
	requestID int64
}

func (h *handler) assign(
	ctx context.Context,
	workerID string,
	conn *sql.Conn,
) gonative.CommandResult {
	if conn == nil {
		return gonative.CommandResult{Err: fmt.Errorf("assign worker %q: database connection is required", workerID)}
	}
	started, completed, err := h.service.assign(ctx, workerID, conn, h.requestID)
	if err != nil {
		err = fmt.Errorf("assign worker %q: %w", workerID, err)
	}

	return gonative.CommandResult{
		TransactionStarted:   started,
		TransactionCompleted: completed,
		Err:                  err,
	}
}
