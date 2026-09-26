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
	"encoding/json"
	"net/http"
	"testing"
)

func TestWorkReportsSteps(t *testing.T) {
	defer func(n int) { hashesPerStep = n }(hashesPerStep)
	hashesPerStep = 10
	ts := newTestServer(t, Config{})
	for _, tc := range []struct {
		query string
		want  int
	}{
		{"?steps=5", 5},
		{"", 60},         // default
		{"?steps=0", 60}, // out of range falls back
		{"?steps=601", 60},
		{"?steps=abc", 60},
	} {
		code, body := get(t, ts.URL+"/work"+tc.query)
		if code != http.StatusOK {
			t.Fatalf("%s: status %d: %s", tc.query, code, body)
		}
		var res WorkResult
		if err := json.Unmarshal([]byte(body), &res); err != nil {
			t.Fatalf("%s: decode %q: %v", tc.query, body, err)
		}
		if res.Steps != tc.want || res.BootID == "" || res.Finished.Before(res.Started) {
			t.Errorf("%s: got %+v, want %d steps with a bootID", tc.query, res, tc.want)
		}
	}
}
