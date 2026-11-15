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

// Package atetest provides an in-process TLS ateapi Control server and mTLS
// certificate fixtures bound strictly to 127.0.0.1 for tests.
package atetest

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	ateapipb "go.ate.dev/substrate/api/ateapi/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
)

// FakeControl implements the ateapi Control RPCs used by sre-gateway.
type FakeControl struct {
	ateapipb.UnimplementedControlServer

	mu              sync.Mutex
	actors          map[string]*ateapipb.Actor
	policies        map[string]*ateapipb.EgressPolicy
	deletePollsLeft map[string]int
	deletePollsCfg  int
	suspended       []string
	authHdrs        []string
}

// NewFakeControl constructs an empty FakeControl instance.
func NewFakeControl() *FakeControl {
	return &FakeControl{
		actors:          make(map[string]*ateapipb.Actor),
		policies:        make(map[string]*ateapipb.EgressPolicy),
		deletePollsLeft: make(map[string]int),
	}
}

// SetDeletePollCount configures DeleteActor to transition actors to ACTOR_STATE_DELETING
// for polls subsequent GetActor calls before removing them, modeling asynchronous teardown.
func (f *FakeControl) SetDeletePollCount(polls int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.deletePollsCfg = polls
}

// MarkDeleting transitions an existing Actor to ACTOR_STATE_DELETING for polls GetActor calls
// before removing it, modeling an external `kubectl ate delete actor` in flight.
func (f *FakeControl) MarkDeleting(atespace, name string, polls int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	k := actorKey(atespace, name)
	a, ok := f.actors[k]
	if !ok {
		return
	}
	a.Status = &ateapipb.ActorStatus{
		State: ateapipb.ActorState_ACTOR_STATE_DELETING,
	}
	f.deletePollsLeft[k] = polls
}

func actorKey(atespace, name string) string { return atespace + "/" + name }

// Actor returns a cloned snapshot of an Actor if present.
func (f *FakeControl) Actor(atespace, name string) *ateapipb.Actor {
	f.mu.Lock()
	defer f.mu.Unlock()
	a, ok := f.actors[actorKey(atespace, name)]
	if !ok {
		return nil
	}
	return proto.Clone(a).(*ateapipb.Actor)
}

// Policy returns a cloned snapshot of an Actor's default EgressPolicy.
func (f *FakeControl) Policy(atespace, name string) *ateapipb.EgressPolicy {
	f.mu.Lock()
	defer f.mu.Unlock()
	p, ok := f.policies[actorKey(atespace, name)]
	if !ok {
		return nil
	}
	return proto.Clone(p).(*ateapipb.EgressPolicy)
}

// MarkRunning transitions an Actor to ACTOR_STATE_RUNNING on workerPod.
func (f *FakeControl) MarkRunning(atespace, name, workerPod string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	a, ok := f.actors[actorKey(atespace, name)]
	if !ok {
		return
	}
	a.Status = &ateapipb.ActorStatus{
		State: ateapipb.ActorState_ACTOR_STATE_RUNNING,
		WorkerAssignment: &ateapipb.WorkerAssignment{
			WorkerPod: workerPod,
		},
	}
}

func (f *FakeControl) CreateActor(_ context.Context, req *ateapipb.CreateActorRequest) (*ateapipb.Actor, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	in := req.GetActor()
	k := actorKey(in.GetMetadata().GetAtespace(), in.GetMetadata().GetName())
	if _, exists := f.actors[k]; exists {
		return nil, status.Error(codes.AlreadyExists, "actor already exists")
	}
	stored := proto.Clone(in).(*ateapipb.Actor)
	stored.Metadata.Uid = "uid-" + k
	stored.Status = &ateapipb.ActorStatus{
		State: ateapipb.ActorState_ACTOR_STATE_SUSPENDED,
	}
	f.actors[k] = stored
	return proto.Clone(stored).(*ateapipb.Actor), nil
}

func (f *FakeControl) GetActor(_ context.Context, req *ateapipb.GetActorRequest) (*ateapipb.Actor, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	k := actorKey(req.GetActor().GetAtespace(), req.GetActor().GetName())
	a, ok := f.actors[k]
	if !ok {
		return nil, status.Error(codes.NotFound, "actor not found")
	}
	if rem, deleting := f.deletePollsLeft[k]; deleting {
		if rem <= 0 {
			delete(f.deletePollsLeft, k)
			delete(f.actors, k)
			delete(f.policies, k)
			return nil, status.Error(codes.NotFound, "actor not found")
		}
		f.deletePollsLeft[k] = rem - 1
	}
	return proto.Clone(a).(*ateapipb.Actor), nil
}

func (f *FakeControl) ListActors(_ context.Context, req *ateapipb.ListActorsRequest) (*ateapipb.ListActorsResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	keys := make([]string, 0, len(f.actors))
	for k, a := range f.actors {
		if a.GetMetadata().GetAtespace() == req.GetAtespace() {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	out := make([]*ateapipb.Actor, 0, len(keys))
	for _, k := range keys {
		out = append(out, proto.Clone(f.actors[k]).(*ateapipb.Actor))
	}
	return &ateapipb.ListActorsResponse{Actors: out}, nil
}

func (f *FakeControl) SuspendActor(_ context.Context, req *ateapipb.SuspendActorRequest) (*ateapipb.SuspendActorResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	k := actorKey(req.GetActor().GetAtespace(), req.GetActor().GetName())
	a, ok := f.actors[k]
	if !ok {
		return nil, status.Error(codes.NotFound, "actor not found")
	}
	a.Status = &ateapipb.ActorStatus{
		State: ateapipb.ActorState_ACTOR_STATE_SUSPENDED,
	}
	f.suspended = append(f.suspended, k)
	return &ateapipb.SuspendActorResponse{}, nil
}

func (f *FakeControl) DeleteActor(_ context.Context, req *ateapipb.DeleteActorRequest) (*ateapipb.Actor, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	k := actorKey(req.GetActor().GetAtespace(), req.GetActor().GetName())
	a, ok := f.actors[k]
	if !ok {
		return nil, status.Error(codes.NotFound, "actor not found")
	}
	if f.deletePollsCfg > 0 {
		a.Status = &ateapipb.ActorStatus{
			State: ateapipb.ActorState_ACTOR_STATE_DELETING,
		}
		if _, already := f.deletePollsLeft[k]; !already {
			f.deletePollsLeft[k] = f.deletePollsCfg
		}
		return proto.Clone(a).(*ateapipb.Actor), nil
	}
	delete(f.actors, k)
	delete(f.policies, k)
	return proto.Clone(a).(*ateapipb.Actor), nil
}

func (f *FakeControl) CreateActorEgressPolicy(_ context.Context, req *ateapipb.CreateActorEgressPolicyRequest) (*ateapipb.EgressPolicy, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	k := actorKey(req.GetActor().GetAtespace(), req.GetActor().GetName())
	if _, exists := f.policies[k]; exists {
		return nil, status.Error(codes.AlreadyExists, "policy already exists")
	}
	stored := proto.Clone(req.GetEgressPolicy()).(*ateapipb.EgressPolicy)
	if stored.Metadata == nil {
		stored.Metadata = &ateapipb.ResourceMetadata{}
	}
	stored.Metadata.Uid = "pol-" + k
	f.policies[k] = stored
	return proto.Clone(stored).(*ateapipb.EgressPolicy), nil
}

func (f *FakeControl) GetActorEgressPolicy(_ context.Context, req *ateapipb.GetActorEgressPolicyRequest) (*ateapipb.EgressPolicy, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	k := actorKey(req.GetActor().GetAtespace(), req.GetActor().GetName())
	p, ok := f.policies[k]
	if !ok {
		return nil, status.Error(codes.NotFound, "policy not found")
	}
	return proto.Clone(p).(*ateapipb.EgressPolicy), nil
}

func (f *FakeControl) UpdateActorEgressPolicy(_ context.Context, req *ateapipb.UpdateActorEgressPolicyRequest) (*ateapipb.EgressPolicy, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	k := actorKey(req.GetActor().GetAtespace(), req.GetActor().GetName())
	cur, ok := f.policies[k]
	if !ok {
		return nil, status.Error(codes.NotFound, "policy not found")
	}
	if req.GetEgressPolicy().GetMetadata().GetUid() != cur.GetMetadata().GetUid() {
		return nil, status.Error(codes.FailedPrecondition, "uid mismatch")
	}
	stored := proto.Clone(req.GetEgressPolicy()).(*ateapipb.EgressPolicy)
	f.policies[k] = stored
	return proto.Clone(stored).(*ateapipb.EgressPolicy), nil
}

func (f *FakeControl) authInterceptor(ctx context.Context, req any, _ *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
	md, _ := metadata.FromIncomingContext(ctx)
	vals := md.Get("authorization")
	tok := ""
	if len(vals) > 0 {
		tok = vals[0]
	}
	if !strings.HasPrefix(tok, "Bearer ") {
		return nil, status.Error(codes.Unauthenticated, "missing Bearer token")
	}
	f.mu.Lock()
	f.authHdrs = append(f.authHdrs, tok)
	f.mu.Unlock()
	return handler(ctx, req)
}

// StartServer starts FakeControl on 127.0.0.1:0 over TLS and writes CA and token files.
func StartServer(t *testing.T, f *FakeControl) (addr, caFile, tokenFile string) {
	t.Helper()
	serverTLS, _, caPEM := buildCAAndCerts(t, "api.ate-system.svc", false)
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen 127.0.0.1: %v", err)
	}
	srv := grpc.NewServer(
		grpc.Creds(credentials.NewTLS(serverTLS)),
		grpc.UnaryInterceptor(f.authInterceptor),
	)
	ateapipb.RegisterControlServer(srv, f)
	go func() { _ = srv.Serve(lis) }()
	t.Cleanup(srv.Stop)

	dir := t.TempDir()
	caFile = filepath.Join(dir, "trust-bundle.pem")
	if err := os.WriteFile(caFile, caPEM, 0o600); err != nil {
		t.Fatal(err)
	}
	tokenFile = filepath.Join(dir, "token")
	if err := os.WriteFile(tokenFile, []byte("projected-sa-jwt-token\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return lis.Addr().String(), caFile, tokenFile
}

// MutualTLSFiles generates a CA, server TLS config requiring client certs, and a
// combined PEM client credential bundle modeling podidentity.podcert.ate.dev/identity.
func MutualTLSFiles(t *testing.T, serverDNS string) (serverTLS *tls.Config, clientBundleFile, caFile string) {
	t.Helper()
	srvTLS, clientBundlePEM, caPEM := buildCAAndCerts(t, serverDNS, true)
	dir := t.TempDir()
	clientBundleFile = filepath.Join(dir, "credential-bundle.pem")
	if err := os.WriteFile(clientBundleFile, clientBundlePEM, 0o600); err != nil {
		t.Fatal(err)
	}
	caFile = filepath.Join(dir, "trust-bundle.pem")
	if err := os.WriteFile(caFile, caPEM, 0o600); err != nil {
		t.Fatal(err)
	}
	return srvTLS, clientBundleFile, caFile
}

func buildCAAndCerts(t *testing.T, serverDNS string, withClient bool) (*tls.Config, []byte, []byte) {
	t.Helper()
	caKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	caTmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "ate-test-ca"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(time.Hour),
		IsCA:                  true,
		BasicConstraintsValid: true,
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature,
	}
	caDER, err := x509.CreateCertificate(rand.Reader, caTmpl, caTmpl, &caKey.PublicKey, caKey)
	if err != nil {
		t.Fatal(err)
	}
	caCert, err := x509.ParseCertificate(caDER)
	if err != nil {
		t.Fatal(err)
	}
	caPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: caDER})

	srvKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	srvTmpl := &x509.Certificate{
		SerialNumber: big.NewInt(2),
		Subject:      pkix.Name{CommonName: serverDNS},
		DNSNames:     []string{serverDNS, "localhost"},
		IPAddresses:  []net.IP{net.ParseIP("127.0.0.1")},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	srvDER, err := x509.CreateCertificate(rand.Reader, srvTmpl, caCert, &srvKey.PublicKey, caKey)
	if err != nil {
		t.Fatal(err)
	}
	srvKeyDER, err := x509.MarshalECPrivateKey(srvKey)
	if err != nil {
		t.Fatal(err)
	}
	srvCertPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: srvDER})
	srvKeyPEM := pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: srvKeyDER})
	srvPair, err := tls.X509KeyPair(srvCertPEM, srvKeyPEM)
	if err != nil {
		t.Fatal(err)
	}

	pool := x509.NewCertPool()
	pool.AppendCertsFromPEM(caPEM)
	srvTLS := &tls.Config{
		Certificates: []tls.Certificate{srvPair},
		MinVersion:   tls.VersionTLS12,
	}

	var clientBundle []byte
	if withClient {
		srvTLS.ClientCAs = pool
		srvTLS.ClientAuth = tls.RequireAndVerifyClientCert

		cliKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
		if err != nil {
			t.Fatal(err)
		}
		spiffeURI, _ := url.Parse("spiffe://cluster.local/ns/workshop-301/sa/sre-gateway")
		cliTmpl := &x509.Certificate{
			SerialNumber: big.NewInt(3),
			Subject:      pkix.Name{CommonName: "sre-gateway"},
			URIs:         []*url.URL{spiffeURI},
			NotBefore:    time.Now().Add(-time.Hour),
			NotAfter:     time.Now().Add(time.Hour),
			KeyUsage:     x509.KeyUsageDigitalSignature,
			ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
		}
		cliDER, err := x509.CreateCertificate(rand.Reader, cliTmpl, caCert, &cliKey.PublicKey, caKey)
		if err != nil {
			t.Fatal(err)
		}
		cliKeyDER, err := x509.MarshalECPrivateKey(cliKey)
		if err != nil {
			t.Fatal(err)
		}
		clientBundle = append(
			pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: cliDER}),
			pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: cliKeyDER})...,
		)
	}
	return srvTLS, clientBundle, caPEM
}
