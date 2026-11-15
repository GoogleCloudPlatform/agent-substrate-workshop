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

package evals

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/pflag"
)

type cliOptions struct {
	gatewayURL string
	adminToken string
	minScore   float64
	outputFile string
	jsonOnly   bool
	help       bool
}

// RunCLI executes the pure-Go online evals runner against sre-gateway and returns an exit code.
func RunCLI(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	opts, err := parseCLIArgs(args)
	if err != nil {
		fmt.Fprintf(stderr, "evalrunner: %v\n", err)
		return 2
	}
	if opts.help {
		printUsage(stdout)
		return 0
	}

	reqBody, _ := json.Marshal(map[string]any{
		"threshold": opts.minScore,
	})
	endpoint := strings.TrimRight(opts.gatewayURL, "/") + "/api/evals/run"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(reqBody))
	if err != nil {
		fmt.Fprintf(stderr, "evalrunner: build request: %v\n", err)
		return 2
	}
	req.Header.Set("Content-Type", "application/json")
	if opts.adminToken != "" {
		req.Header.Set("X-Broker-Admin-Token", opts.adminToken)
	}

	hc := &http.Client{Timeout: 90 * time.Second}
	resp, err := hc.Do(req)
	if err != nil {
		fmt.Fprintf(stderr, "evalrunner: reach sre-gateway (%s): %v\n", endpoint, err)
		return 2
	}
	defer resp.Body.Close()

	rawResp, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		fmt.Fprintf(stderr, "evalrunner: HTTP %d from %s: %s\n", resp.StatusCode, endpoint, strings.TrimSpace(string(rawResp)))
		return 2
	}

	var report Report
	if err := json.Unmarshal(rawResp, &report); err != nil {
		fmt.Fprintf(stderr, "evalrunner: decode evaluation report: %v\n", err)
		return 2
	}

	if opts.outputFile != "" {
		if err := writeReportFile(opts.outputFile, report); err != nil {
			fmt.Fprintf(stderr, "evalrunner: write %s: %v\n", opts.outputFile, err)
			return 2
		}
	}

	if opts.jsonOnly {
		enc := json.NewEncoder(stdout)
		enc.SetIndent("", "  ")
		_ = enc.Encode(report)
	} else {
		renderHumanReport(stdout, report)
	}

	if !report.Passed || report.OverallScore < opts.minScore {
		fmt.Fprintf(stderr, "evalrunner: online evaluation FAILED (overall=%.2f, threshold=%.2f)\n", report.OverallScore, opts.minScore)
		return 1
	}
	return 0
}

func parseCLIArgs(args []string) (cliOptions, error) {
	defaultGateway := firstEnv("EVAL_GATEWAY_URL", "BROKER_URL")
	if defaultGateway == "" {
		defaultGateway = "http://127.0.0.1:8081"
	}

	var opts cliOptions
	fs := pflag.NewFlagSet("evalrunner", pflag.ContinueOnError)
	fs.SetOutput(io.Discard)
	fs.StringVar(&opts.gatewayURL, "gateway-url", defaultGateway, "Base URL of sre-gateway")
	fs.StringVar(&opts.adminToken, "admin-token", strings.TrimSpace(os.Getenv("BROKER_ADMIN_TOKEN")), "Optional X-Broker-Admin-Token")
	fs.Float64Var(&opts.minScore, "min-score", DefaultThreshold, "Minimum passing score in (0.0, 1.0]")
	fs.StringVarP(&opts.outputFile, "output", "o", "", "Optional path to write JSON evaluation report")
	fs.BoolVar(&opts.jsonOnly, "json", false, "Print JSON report to stdout")
	fs.BoolVarP(&opts.help, "help", "h", false, "Show usage")

	if err := fs.Parse(args); err != nil {
		return opts, err
	}
	if opts.help {
		return opts, nil
	}
	if fs.NArg() > 0 {
		return opts, fmt.Errorf("unexpected argument %q", fs.Arg(0))
	}

	opts.gatewayURL = strings.TrimSpace(opts.gatewayURL)
	opts.adminToken = strings.TrimSpace(opts.adminToken)
	opts.outputFile = strings.TrimSpace(opts.outputFile)

	if opts.gatewayURL == "" {
		return opts, errors.New("--gateway-url is required")
	}
	if opts.minScore <= 0 || opts.minScore > 1.0 {
		return opts, errors.New("--min-score must be a number in (0.0, 1.0]")
	}
	return opts, nil
}

func writeReportFile(outPath string, rep Report) error {
	clean := filepath.Clean(outPath)
	if clean == "." || clean == "/" {
		return errors.New("invalid output path")
	}
	if dir := filepath.Dir(clean); dir != "." && dir != "" {
		if err := os.MkdirAll(dir, 0o750); err != nil {
			return err
		}
	}
	data, err := json.MarshalIndent(rep, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	return os.WriteFile(clean, data, 0o600)
}

func renderHumanReport(w io.Writer, rep Report) {
	verdict := "PASS"
	if !rep.Passed {
		verdict = "FAIL"
	}
	fmt.Fprintf(w, "Online evaluation verdict: %s  overall=%.2f  threshold=%.2f  judge=%s (%s)\n\n",
		verdict, rep.OverallScore, rep.Threshold, rep.JudgeBackend, rep.Model)

	tw := tabwriter.NewWriter(w, 0, 2, 2, ' ', 0)
	fmt.Fprintln(tw, "RUBRIC\tSCORE\tTHRESHOLD\tSTATUS\tRATIONALE")
	for _, r := range rep.Rubrics {
		st := "PASS"
		if !r.Passed {
			st = "FAIL"
		}
		fmt.Fprintf(tw, "%s\t%.2f\t%.2f\t%s\t%s\n", r.ID, r.Score, r.Threshold, st, r.Rationale)
	}
	_ = tw.Flush()
}

func printUsage(w io.Writer) {
	fmt.Fprintln(w, "Usage: evalrunner [--gateway-url URL] [--min-score 0.80] [--output FILE] [--json]")
}

func firstEnv(keys ...string) string {
	for _, k := range keys {
		if v := strings.TrimSpace(os.Getenv(k)); v != "" {
			return v
		}
	}
	return ""
}
