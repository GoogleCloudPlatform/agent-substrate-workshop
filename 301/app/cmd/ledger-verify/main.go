// Copyright 2026 Google LLC
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

// Command ledger-verify checks a cloned ledger-api workspace for the connection
// pool configuration and row-cursor leak fixes without invoking the Go compiler
// inside a memory-constrained sandbox.
package main

import (
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/gke-demos/substrate-agent-platform/app/internal/ledgerverify"
	"github.com/spf13/pflag"
)

const defaultWorkspaceDir = "/workspace/ledger-api"

func main() {
	dir, showHelp, err := parseArgs(os.Args[1:])
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\nUsage: ledger-verify [--dir <path>] [<path>]\n", err)
		os.Exit(2)
	}
	if showHelp {
		fmt.Println("Usage: ledger-verify [--dir <path>] [<path>]")
		return
	}

	rep, err := ledgerverify.VerifyDir(dir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: verification failed for %s: %v\n", dir, err)
		os.Exit(1)
	}
	fmt.Printf("VERIFIED %s (max_open_conns=%d idle_timeout_seconds=%d defer_rows_close=%t)\n",
		dir, rep.MaxOpenConns, rep.IdleTimeoutSeconds, rep.DeferClosePresent)
}

func parseArgs(args []string) (string, bool, error) {
	fs := pflag.NewFlagSet("ledger-verify", pflag.ContinueOnError)
	fs.SetOutput(io.Discard)

	var dir string
	var showHelp bool
	fs.StringVarP(&dir, "dir", "d", "", "Path to cloned ledger-api workspace")
	fs.BoolVarP(&showHelp, "help", "h", false, "Show usage")

	if err := fs.Parse(args); err != nil {
		return "", false, err
	}
	if showHelp {
		return "", true, nil
	}
	dir = strings.TrimSpace(dir)
	if strings.HasPrefix(dir, "-") {
		return "", false, fmt.Errorf("missing value for -d/--dir (got flag %s)", dir)
	}
	switch fs.NArg() {
	case 0:
	case 1:
		if dir != "" {
			return "", false, fmt.Errorf("unexpected extra argument %s", fs.Arg(0))
		}
		dir = strings.TrimSpace(fs.Arg(0))
	default:
		return "", false, fmt.Errorf("unexpected extra argument %s", fs.Arg(1))
	}
	if dir == "" {
		dir = defaultWorkspaceDir
	}
	return dir, false, nil
}
