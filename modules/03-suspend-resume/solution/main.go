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
		DataDir:  "/data",
		ModelURL: "",
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
