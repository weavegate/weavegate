// Command weavegate reaches a verdict on a configured concurrent workflow
// and saves the run evidence to disk.
package main

import (
	"os"

	"github.com/weavegate/weavegate/internal/ci"
)

func main() {
	err := Execute(os.Args[1:], os.Stdout, os.Stderr)
	os.Exit(ci.ExitCode(err, ci.Verdict{}))
}
