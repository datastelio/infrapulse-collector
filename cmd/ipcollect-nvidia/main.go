// Command ipcollect-nvidia samples NVIDIA GPU metrics with nvidia-smi and
// appends them to a local CSV or JSON Lines file.
package main

import (
	"os"

	"github.com/datastelio/infrapulse-collector/internal/collector"
	"github.com/datastelio/infrapulse-collector/internal/nvidia"
)

func main() {
	os.Exit(collector.Main("ipcollect-nvidia", "nvidia", nvidia.Open, os.Args[1:]))
}
