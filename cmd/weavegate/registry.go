package main

import (
	"io/fs"
	"sort"

	matchingfixture "github.com/weavegate/weavegate/fixtures/matching-slice"
	matchingsut "github.com/weavegate/weavegate/fixtures/matching-slice/sut"
	"github.com/weavegate/weavegate/internal/sut"
	"github.com/weavegate/weavegate/internal/sut/gonative"
	"github.com/weavegate/weavegate/internal/syncpoint"
)

// entrypoint declares one built-in workflow the CLI can run. An entrypoint
// is a Go function, not a path (A-3): the gonative adapter cannot load a
// workflow dynamically, so target.sut.entrypoint names one of these IDs.
type entrypoint struct {
	NewAdapter func(syncpoint.Client) sut.Adapter
	Variants   []string
	Schedules  fs.FS
}

// builtinEntrypoints is the CLI's fixture registry. Only cmd/weavegate knows
// about fixtures; the engine packages never import them (AGENTS.md: fixtures
// are data).
var builtinEntrypoints = map[string]entrypoint{
	"matching-slice": {
		NewAdapter: func(client syncpoint.Client) sut.Adapter {
			return gonative.New(matchingsut.NewRegistry(client))
		},
		Variants:  []string{"vulnerable", "fixed"},
		Schedules: matchingfixture.ScheduleFS(),
	},
}

// knownEntrypointIDs returns the built-in entrypoint IDs in sorted order for
// use in error messages.
func knownEntrypointIDs() []string {
	ids := make([]string, 0, len(builtinEntrypoints))
	for id := range builtinEntrypoints {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}
