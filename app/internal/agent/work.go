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

	"github.com/agent-substrate/workshop/app/internal/chatapi"
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

// heartbeatEvery is how often a job with Heartbeat on POSTs its Busy-URL. It has to be shorter than
// the gateway's IDLE_AFTER (3s in Module 5).
var heartbeatEvery = time.Second

// WorkResult is what GET /work reports.
type WorkResult struct {
	Steps     int       `json:"steps"`
	Started   time.Time `json:"started"`
	Finished  time.Time `json:"finished"`
	WallSec   float64   `json:"wallSec"`
	MaxGapSec float64   `json:"maxGapSec"` // longest wall time between two steps; a freeze shows up here
	BootID    string    `json:"bootID"`
}

// stepsParam reads ?steps=N: 1..600, default 60.
func stepsParam(r *http.Request) int {
	n, err := strconv.Atoi(r.URL.Query().Get("steps"))
	if err != nil || n <= 0 || n > 600 {
		return 60
	}
	return n
}

// work burns CPU for steps steps and logs each one, so a freeze is visible in the actor's log.
func (s *Server) work(kind string, steps int) WorkResult {
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
		log.Printf("%s step %d/%d (gap %.1fs)", kind, i, steps, gap)
	}
	res.Finished = time.Now().UTC()
	res.WallSec = res.Finished.Sub(res.Started).Seconds()
	log.Printf("%s done: %d steps in %.1fs, max gap %.1fs", kind, steps, res.WallSec, res.MaxGapSec)
	return res
}

// handleWork serves GET /work?steps=N: the work happens inside the request, and the answer comes
// when it's done. Module 5 uses it as a slow request the gateway's idle suspender can't see.
func (s *Server) handleWork(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.work("work", stepsParam(r)))
}

// handleJob serves POST /jobs?steps=N: it answers 202 right away and does the work in the
// background, where no request is in flight to keep the actor awake. With Heartbeat on and a
// Busy-URL header, the job POSTs that URL every heartbeatEvery until it finishes (optional lab I).
func (s *Server) handleJob(w http.ResponseWriter, r *http.Request) {
	steps, busyURL := stepsParam(r), r.Header.Get(chatapi.BusyURLHeader)
	heartbeat := s.cfg.Heartbeat && busyURL != ""
	go func() {
		if heartbeat {
			stop := s.heartbeat(busyURL)
			defer stop()
		}
		s.work("job", steps)
	}()
	writeJSON(w, http.StatusAccepted, map[string]any{"steps": steps, "heartbeat": heartbeat})
}

// heartbeat POSTs url every heartbeatEvery until stop is called.
func (s *Server) heartbeat(url string) (stop func()) {
	done := make(chan struct{})
	go func() {
		t := time.NewTicker(heartbeatEvery)
		defer t.Stop()
		for {
			select {
			case <-done:
				return
			case <-t.C:
				resp, err := s.cfg.Client.Post(url, "text/plain", nil)
				if err != nil {
					log.Printf("heartbeat: %v", err)
					continue
				}
				resp.Body.Close()
				if resp.StatusCode != http.StatusNoContent {
					log.Printf("heartbeat: %s", resp.Status)
				}
			}
		}
	}()
	return func() { close(done) }
}
