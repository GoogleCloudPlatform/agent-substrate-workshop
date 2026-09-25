// Package testutil provides a fake ateapi Control server over real TLS for tests.
package testutil

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
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/agent-substrate/substrate/pkg/proto/ateapipb"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
)

// SelfSignedTLS returns a server TLS config for dnsName (and localhost/127.0.0.1) and the PEM
// of its self-signed certificate, which clients use as their CA.
func SelfSignedTLS(t *testing.T, dnsName string) (*tls.Config, []byte) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: dnsName},
		DNSNames:              []string{dnsName, "localhost"},
		IPAddresses:           []net.IP{net.ParseIP("127.0.0.1")},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(time.Hour),
		IsCA:                  true,
		BasicConstraintsValid: true,
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})
	pair, err := tls.X509KeyPair(certPEM, keyPEM)
	if err != nil {
		t.Fatal(err)
	}
	return &tls.Config{Certificates: []tls.Certificate{pair}, MinVersion: tls.VersionTLS12}, certPEM
}

// NewActor builds an Actor record the way ateapi returns it.
func NewActor(atespace, name string, st ateapipb.ActorState) *ateapipb.Actor {
	return &ateapipb.Actor{
		Metadata: &ateapipb.ResourceMetadata{Atespace: atespace, Name: name},
		Status:   &ateapipb.ActorStatus{State: st},
	}
}

// FakeControl implements the ateapi calls the workshop uses. PageCap > 0 forces pagination.
type FakeControl struct {
	ateapipb.UnimplementedControlServer
	Actors     []*ateapipb.Actor
	PageCap    int
	SuspendErr error

	mu        sync.Mutex
	policies  map[string]*ateapipb.EgressPolicy
	suspended []string
	auth      []string
}

func key(ref *ateapipb.ObjectRef) string { return ref.GetAtespace() + "/" + ref.GetName() }

func (f *FakeControl) Suspended() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.suspended...)
}

func (f *FakeControl) Auth() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.auth...)
}

func (f *FakeControl) Policy(atespace, name string) *ateapipb.EgressPolicy {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.policies[atespace+"/"+name]
}

func (f *FakeControl) ListActors(_ context.Context, req *ateapipb.ListActorsRequest) (*ateapipb.ListActorsResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var in []*ateapipb.Actor
	for _, a := range f.Actors {
		if a.GetMetadata().GetAtespace() == req.GetAtespace() {
			in = append(in, a)
		}
	}
	start, _ := strconv.Atoi(req.GetPageToken())
	size := int(req.GetPageSize())
	if f.PageCap > 0 && (size == 0 || size > f.PageCap) {
		size = f.PageCap
	}
	end := len(in)
	if size > 0 && start+size < end {
		end = start + size
	}
	resp := &ateapipb.ListActorsResponse{Actors: in[start:end]}
	if end < len(in) {
		resp.NextPageToken = strconv.Itoa(end)
	}
	return resp, nil
}

func (f *FakeControl) SuspendActor(_ context.Context, req *ateapipb.SuspendActorRequest) (*ateapipb.SuspendActorResponse, error) {
	if f.SuspendErr != nil {
		return nil, f.SuspendErr
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.suspended = append(f.suspended, key(req.GetActor()))
	for _, a := range f.Actors {
		if a.GetMetadata().GetAtespace() == req.GetActor().GetAtespace() && a.GetMetadata().GetName() == req.GetActor().GetName() {
			a.Status.State = ateapipb.ActorState_ACTOR_STATE_SUSPENDED
		}
	}
	return &ateapipb.SuspendActorResponse{}, nil
}

func (f *FakeControl) CreateActorEgressPolicy(_ context.Context, req *ateapipb.CreateActorEgressPolicyRequest) (*ateapipb.EgressPolicy, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.policies == nil {
		f.policies = map[string]*ateapipb.EgressPolicy{}
	}
	k := key(req.GetActor())
	if _, ok := f.policies[k]; ok {
		return nil, status.Error(codes.AlreadyExists, "policy exists")
	}
	p := proto.Clone(req.GetEgressPolicy()).(*ateapipb.EgressPolicy)
	p.Metadata.Uid = "uid-" + k
	f.policies[k] = p
	return p, nil
}

func (f *FakeControl) GetActorEgressPolicy(_ context.Context, req *ateapipb.GetActorEgressPolicyRequest) (*ateapipb.EgressPolicy, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	p, ok := f.policies[key(req.GetActor())]
	if !ok {
		return nil, status.Error(codes.NotFound, "no policy")
	}
	return p, nil
}

// UpdateActorEgressPolicy requires the caller to send back the stored metadata (uid), as ateapi does.
func (f *FakeControl) UpdateActorEgressPolicy(_ context.Context, req *ateapipb.UpdateActorEgressPolicyRequest) (*ateapipb.EgressPolicy, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	k := key(req.GetActor())
	cur, ok := f.policies[k]
	if !ok {
		return nil, status.Error(codes.NotFound, "no policy")
	}
	if req.GetEgressPolicy().GetMetadata().GetUid() != cur.GetMetadata().GetUid() {
		return nil, status.Error(codes.FailedPrecondition, "metadata mismatch")
	}
	f.policies[k] = proto.Clone(req.GetEgressPolicy()).(*ateapipb.EgressPolicy)
	return f.policies[k], nil
}

func (f *FakeControl) DeleteActorEgressPolicy(_ context.Context, req *ateapipb.DeleteActorEgressPolicyRequest) (*ateapipb.EgressPolicy, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	k := key(req.GetActor())
	p, ok := f.policies[k]
	if !ok {
		return nil, status.Error(codes.NotFound, "no policy")
	}
	delete(f.policies, k)
	return p, nil
}

func (f *FakeControl) recordAuth(ctx context.Context, req any, _ *grpc.UnaryServerInfo, h grpc.UnaryHandler) (any, error) {
	md, _ := metadata.FromIncomingContext(ctx)
	f.mu.Lock()
	f.auth = append(f.auth, append(md.Get("authorization"), "")[0])
	f.mu.Unlock()
	return h(ctx, req)
}

// StartFake serves f over TLS on 127.0.0.1 and returns its address and a CA file path.
func StartFake(t *testing.T, f *FakeControl) (addr, caFile string) {
	t.Helper()
	serverTLS, caPEM := SelfSignedTLS(t, "api.ate-system.svc")
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	srv := grpc.NewServer(grpc.Creds(credentials.NewTLS(serverTLS)), grpc.UnaryInterceptor(f.recordAuth))
	ateapipb.RegisterControlServer(srv, f)
	go func() { _ = srv.Serve(lis) }()
	t.Cleanup(srv.Stop)
	caFile = filepath.Join(t.TempDir(), "ca.pem")
	if err := os.WriteFile(caFile, caPEM, 0o600); err != nil {
		t.Fatal(err)
	}
	return lis.Addr().String(), caFile
}
