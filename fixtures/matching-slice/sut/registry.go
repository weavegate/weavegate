package matchingsut

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/weavegate/weavegate/internal/sut"
	"github.com/weavegate/weavegate/internal/sut/gonative"
)

const CommandAssign = "assign"

// Registry creates matching assignment commands for the selected variant.
type Registry struct {
	syncPoint SyncPoint
}

var _ gonative.Registry = (*Registry)(nil)

// NewRegistry returns a matching command registry. A nil sync-point uses the
// production no-op behavior.
func NewRegistry(syncPoint SyncPoint) *Registry {
	if syncPoint == nil {
		syncPoint = NoopSyncPoint{}
	}
	return &Registry{syncPoint: syncPoint}
}

func (r *Registry) Commands(cfg sut.SUTConfig) (map[string]gonative.CommandFunc, error) {
	selected := variant(strings.TrimSpace(cfg.Variant))
	repository, err := newRepository(selected)
	if err != nil {
		return nil, err
	}

	requestIDText := strings.TrimSpace(cfg.Params["request_id"])
	requestID, err := strconv.ParseInt(requestIDText, 10, 64)
	if err != nil || requestID <= 0 {
		return nil, fmt.Errorf("configure matching command: invalid request_id %q", requestIDText)
	}

	handler := &handler{
		service: &service{
			repository: repository,
			syncPoint:  r.syncPoint,
		},
		requestID: requestID,
	}

	return map[string]gonative.CommandFunc{
		CommandAssign: handler.assign,
	}, nil
}
