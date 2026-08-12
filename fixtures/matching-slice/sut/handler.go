package matchingsut

import (
	"context"
	"database/sql"
	"fmt"
)

type handler struct {
	service   *service
	requestID int64
}

func (h *handler) assign(
	ctx context.Context,
	workerID string,
	conn *sql.Conn,
) error {
	if conn == nil {
		return fmt.Errorf("assign worker %q: database connection is required", workerID)
	}
	if err := h.service.assign(ctx, workerID, conn, h.requestID); err != nil {
		return fmt.Errorf("assign worker %q: %w", workerID, err)
	}

	return nil
}
