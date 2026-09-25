package main

import (
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/agent-substrate/workshop/app/internal/ateclient"
)

type config struct {
	ListenAddr     string
	Atespace       string
	RouterURL      string
	MockLLM        bool
	GeminiModel    string
	Project        string
	VertexLocation string
	ATE            ateclient.Options
	IdleAfter      time.Duration
	IdleCheckEvery time.Duration
}

func loadConfig(getenv func(string) string) (config, error) {
	or := func(k, d string) string {
		if v := getenv(k); v != "" {
			return v
		}
		return d
	}
	var errs []error
	boolean := func(k string) bool {
		v := or(k, "false")
		b, err := strconv.ParseBool(v)
		if err != nil {
			errs = append(errs, fmt.Errorf("%s=%q is not a boolean", k, v))
		}
		return b
	}
	duration := func(k, d string) time.Duration {
		v := or(k, d)
		dur, err := time.ParseDuration(v)
		if err != nil {
			errs = append(errs, fmt.Errorf("%s=%q is not a duration (e.g. 20s)", k, v))
		}
		return dur
	}
	c := config{
		ListenAddr:     or("LISTEN_ADDR", ":8080"),
		Atespace:       getenv("ATESPACE"),
		RouterURL:      or("ROUTER_URL", "http://atenet-router.ate-system.svc.cluster.local"),
		MockLLM:        boolean("MOCK_LLM"),
		GeminiModel:    or("GEMINI_MODEL", "gemini-2.5-flash"),
		Project:        getenv("GOOGLE_CLOUD_PROJECT"),
		VertexLocation: or("VERTEX_LOCATION", "us-central1"),
		ATE: ateclient.Options{
			Addr:      or("ATE_API_ADDR", "api.ate-system.svc.cluster.local:443"),
			TokenFile: or("ATE_API_TOKEN_FILE", "/var/run/ate/token/token"),
			CAFile:    or("ATE_API_CA_FILE", "/var/run/ate/trust/trust-bundle.pem"),
			Insecure:  boolean("ATE_API_INSECURE"),
		},
		IdleAfter:      duration("IDLE_AFTER", "0s"),
		IdleCheckEvery: duration("IDLE_CHECK_EVERY", "5s"),
	}
	if c.Atespace == "" {
		errs = append(errs, errors.New("ATESPACE is required"))
	}
	if c.IdleCheckEvery <= 0 {
		errs = append(errs, errors.New("IDLE_CHECK_EVERY must be positive"))
	}
	return c, errors.Join(errs...)
}
