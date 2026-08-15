// Command weavegate reaches a verdict on a configured concurrent workflow
// and saves the run evidence to disk.
package main

import "os"

func main() {
	os.Exit(Execute(os.Args[1:], os.Stdout, os.Stderr))
}
