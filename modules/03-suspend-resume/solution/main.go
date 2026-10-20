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

// Command agent is the actor you build during the workshop. Substrate runs it inside a
// gVisor sandbox on a worker pod and routes HTTP to it on port 80.
//
// Each module asks you to change one line marked WORKSHOP. Finished versions live in
// modules/NN-*/solution/main.go.
package main

import (
	"log"
	"net/http"
	"os"

	"github.com/agent-substrate/workshop/app/internal/agent"
)

func main() {
	cfg := agent.Config{
		// module-2: custom greeting.
		Greeting: "hello from my first actor",
		// module-3: state on the durableDir.
		DataDir: "/data",
		// WORKSHOP(module-4): set ModelURL to os.Getenv("MODEL_URL") so the agent asks the gateway's model.
		ModelURL: "",
		// WORKSHOP(lab-c): set Heartbeat to true so background jobs tell the gateway they're busy.
		Heartbeat: false,
	}

	srv, err := agent.New(cfg)
	if err != nil {
		log.Fatalf("agent: %v", err)
	}
	addr := os.Getenv("LISTEN_ADDR") // unset inside Substrate; set it to run locally, e.g. LISTEN_ADDR=:8080
	if addr == "" {
		addr = ":80"
	}
	log.Printf("agent listening on %s (dataDir=%q modelURL=%q)", addr, cfg.DataDir, cfg.ModelURL)
	log.Fatal(http.ListenAndServe(addr, srv.Handler()))
}
