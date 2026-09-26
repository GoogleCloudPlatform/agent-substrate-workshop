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

package ateclient

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/agent-substrate/substrate/pkg/proto/ateapipb"
	"github.com/agent-substrate/workshop/app/internal/testutil"
)

func writeFile(t *testing.T, name, content string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestTLSConfigDefaultsServerName(t *testing.T) {
	c, err := TLSConfig(Options{})
	if err != nil || c.ServerName != DefaultServerName {
		t.Fatalf("TLSConfig = %v, %v", c, err)
	}
}

func TestTLSConfigRejectsFileWithoutCerts(t *testing.T) {
	if _, err := TLSConfig(Options{CAFile: writeFile(t, "ca.pem", "not a cert")}); err == nil {
		t.Fatal("want error for CA file without certificates")
	}
}

func TestTLSConfigMissingCAFile(t *testing.T) {
	if _, err := TLSConfig(Options{CAFile: "/nonexistent/ca.pem"}); err == nil {
		t.Fatal("want error for missing CA file")
	}
}

func TestFileTokenRereadsOnEveryCall(t *testing.T) {
	p := writeFile(t, "token", "first\n")
	tok := fileToken{path: p}
	md, err := tok.GetRequestMetadata(context.Background())
	if err != nil || md["authorization"] != "Bearer first" {
		t.Fatalf("md = %v, %v", md, err)
	}
	_ = os.WriteFile(p, []byte("second"), 0o600)
	md, _ = tok.GetRequestMetadata(context.Background())
	if md["authorization"] != "Bearer second" {
		t.Fatalf("token not re-read: %v", md)
	}
}

func TestFileTokenEmptyIsAnError(t *testing.T) {
	if _, err := (fileToken{path: writeFile(t, "token", "  \n")}).GetRequestMetadata(context.Background()); err == nil {
		t.Fatal("want error for empty token file")
	}
}

func TestDialVerifiesServerAndSendsToken(t *testing.T) {
	fake := &testutil.FakeControl{Actors: []*ateapipb.Actor{testutil.NewActor("workshop", "a", ateapipb.ActorState_ACTOR_STATE_RUNNING)}}
	addr, ca := testutil.StartFake(t, fake)
	c, closer, err := Dial(Options{Addr: addr, CAFile: ca, TokenFile: writeFile(t, "token", "s3cret")})
	if err != nil {
		t.Fatal(err)
	}
	defer closer.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	resp, err := c.ListActors(ctx, &ateapipb.ListActorsRequest{Atespace: "workshop"})
	if err != nil || len(resp.GetActors()) != 1 {
		t.Fatalf("ListActors = %v, %v", resp, err)
	}
	if got := fake.Auth(); len(got) != 1 || got[0] != "Bearer s3cret" {
		t.Fatalf("server saw auth %v", got)
	}
}

func TestDialFailsAgainstUntrustedServer(t *testing.T) {
	addr, _ := testutil.StartFake(t, &testutil.FakeControl{})
	c, closer, err := Dial(Options{Addr: addr}) // system roots do not trust the fake's cert
	if err != nil {
		t.Fatal(err)
	}
	defer closer.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := c.ListActors(ctx, &ateapipb.ListActorsRequest{Atespace: "x"}); err == nil {
		t.Fatal("want TLS verification failure")
	}
}

func TestDialInsecureSkipsVerification(t *testing.T) {
	addr, _ := testutil.StartFake(t, &testutil.FakeControl{})
	c, closer, err := Dial(Options{Addr: addr, Insecure: true})
	if err != nil {
		t.Fatal(err)
	}
	defer closer.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := c.ListActors(ctx, &ateapipb.ListActorsRequest{Atespace: "x"}); err != nil {
		t.Fatalf("insecure ListActors: %v", err)
	}
}

func TestDialRequiresAddr(t *testing.T) {
	if _, _, err := Dial(Options{}); err == nil {
		t.Fatal("want error without Addr")
	}
}
