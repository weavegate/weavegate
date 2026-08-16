package matchingslice

import (
	"embed"
	"io/fs"
)

//go:embed schedules/*.json
var embeddedSchedules embed.FS

// ScheduleFS returns committed matching-slice schedules independently of the
// repository checkout and process working directory.
func ScheduleFS() fs.FS {
	schedules, err := fs.Sub(embeddedSchedules, "schedules")
	if err != nil {
		panic(err)
	}
	return schedules
}
