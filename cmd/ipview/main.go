// Command ipview prints an ipcollect output file (CSV or JSON Lines) as a
// terminal table: the latest sample per GPU by default, or every row.
package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/datastelio/infrapulse-collector/internal/sample"
	"github.com/datastelio/infrapulse-collector/internal/view"
)

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

func run(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("ipview", flag.ContinueOnError)
	fs.SetOutput(stderr)
	all := fs.Bool("all", false, "print every row instead of the latest sample per GPU")
	watch := fs.Int("watch", 0, "re-read the file every N seconds (0 = print once)")
	fs.Usage = func() {
		fmt.Fprintln(fs.Output(), "Usage: ipview [--all] [--watch N] [file]   (default file: ./gpu-samples.csv)")
		fs.PrintDefaults()
	}
	// Accept flags before or after the file name.
	var files []string
	for {
		if err := fs.Parse(args); err != nil {
			if errors.Is(err, flag.ErrHelp) {
				return 0
			}
			return 2
		}
		if fs.NArg() == 0 {
			break
		}
		files = append(files, fs.Arg(0))
		args = fs.Args()[1:]
	}
	if len(files) > 1 {
		fmt.Fprintln(stderr, "ipview: give one file")
		return 2
	}
	path := "gpu-samples.csv"
	if len(files) == 1 {
		path = files[0]
	} else if _, err := os.Stat(path); err != nil {
		if _, err := os.Stat("gpu-samples.jsonl"); err == nil {
			path = "gpu-samples.jsonl"
		}
	}

	show := func() error {
		rows, err := sample.ReadFile(path)
		if err != nil {
			return err
		}
		if !*all {
			rows = view.Latest(rows)
		}
		if len(rows) == 0 {
			fmt.Fprintf(stdout, "%s: no samples yet\n", path)
			return nil
		}
		return view.Render(stdout, rows)
	}

	if *watch <= 0 {
		if err := show(); err != nil {
			fmt.Fprintf(stderr, "ipview: %v\n", err)
			return 1
		}
		return 0
	}
	for {
		fmt.Fprint(stdout, "\033[H\033[2J") // clear the screen
		fmt.Fprintf(stdout, "%s  every %ds  %s  (Ctrl-C to quit)\n\n", path, *watch, time.Now().UTC().Format(time.RFC3339))
		if err := show(); err != nil {
			fmt.Fprintf(stdout, "ipview: %v\n", err)
		}
		time.Sleep(time.Duration(*watch) * time.Second)
	}
}
