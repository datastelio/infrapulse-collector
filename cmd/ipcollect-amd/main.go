// Command ipcollect-amd samples AMD GPU metrics with amd-smi (or rocm-smi)
// and appends them to a local CSV or JSON Lines file.
package main

import (
	"os"

	"github.com/datastelio/infrapulse-collector/internal/amd"
	"github.com/datastelio/infrapulse-collector/internal/collector"
)

func main() {
	os.Exit(collector.Main("ipcollect-amd", "amd", amd.Open, os.Args[1:]))
}
