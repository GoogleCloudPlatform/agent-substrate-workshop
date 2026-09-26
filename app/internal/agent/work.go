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

package agent

import (
	"crypto/sha256"
	"log"
	"net/http"
	"os"
	"strconv"
	"time"
)

// hashesPerStep is fixed work, not a wall-clock deadline, so a freeze shows up as a gap between
// steps instead of making the job finish early. Timing a calibration loop doesn't work right after
// a restore, while the sandbox clock catches up. ~0.3s per step on a c3 worker; HASHES_PER_STEP
// overrides it.
var hashesPerStep = func() int {
	if n, err := strconv.Atoi(os.Getenv("HASHES_PER_STEP")); err == nil && n > 0 {
		return n
	}
	return 3_000_000
}()

// WorkResult is what GET /work reports.
type WorkResult struct {
	Steps     int       `json:"steps"`
	Started   time.Time `json:"started"`
	Finished  time.Time `json:"finished"`
	WallSec   float64   `json:"wallSec"`
	MaxGapSec float64   `json:"maxGapSec"` // longest wall time between two steps; a freeze shows up here
	BootID    string    `json:"bootID"`
}

// handleWork burns CPU for ?steps=N (1..600, default 60) inside the request and answers when done.
// Module 5 uses it as a slow request the gateway's idle suspender can't see.
func (s *Server) handleWork(w http.ResponseWriter, r *http.Request) {
	steps, err := strconv.Atoi(r.URL.Query().Get("steps"))
	if err != nil || steps <= 0 || steps > 600 {
		steps = 60
	}
	res := WorkResult{Steps: steps, Started: time.Now().UTC(), BootID: s.bootID}
	prev, sum := res.Started, sha256.Sum256(nil)
	for i := 1; i <= steps; i++ {
		for j := 0; j < hashesPerStep; j++ {
			sum = sha256.Sum256(sum[:])
		}
		now := time.Now().UTC()
		gap := now.Sub(prev).Seconds()
		res.MaxGapSec = max(res.MaxGapSec, gap)
		prev = now
		log.Printf("work step %d/%d (gap %.1fs)", i, steps, gap)
	}
	res.Finished = time.Now().UTC()
	res.WallSec = res.Finished.Sub(res.Started).Seconds()
	log.Printf("work done: %d steps in %.1fs, max gap %.1fs", steps, res.WallSec, res.MaxGapSec)
	writeJSON(w, http.StatusOK, res)
}
