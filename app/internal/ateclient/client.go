// Package ateclient dials the Agent Substrate control API (ateapi) the way in-cluster
// clients do: TLS verified against the ClusterTrustBundle, plus a Kubernetes service
// account token with audience api.ate-system.svc as a bearer token.
package ateclient

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/agent-substrate/substrate/pkg/proto/ateapipb"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
)

// DefaultServerName is the name ateapi's serving certificate is issued for.
const DefaultServerName = "api.ate-system.svc"

// Options says where ateapi is and how to trust and authenticate to it.
type Options struct {
	Addr       string // host:port, e.g. api.ate-system.svc.cluster.local:443 or localhost:8443
	ServerName string // TLS name to verify; defaults to DefaultServerName
	CAFile     string // PEM trust bundle; empty uses system roots
	TokenFile  string // bearer token file, re-read on every call because projected tokens rotate
	Insecure   bool   // skip TLS verification (port-forward fallback only)
}

// TLSConfig builds the client TLS config for o.
func TLSConfig(o Options) (*tls.Config, error) {
	name := o.ServerName
	if name == "" {
		name = DefaultServerName
	}
	cfg := &tls.Config{ServerName: name, MinVersion: tls.VersionTLS12}
	if o.Insecure {
		cfg.InsecureSkipVerify = true //nolint:gosec // explicit opt-in for port-forward debugging
		return cfg, nil
	}
	if o.CAFile != "" {
		pemBytes, err := os.ReadFile(o.CAFile)
		if err != nil {
			return nil, fmt.Errorf("ateclient: read CA: %w", err)
		}
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM(pemBytes) {
			return nil, fmt.Errorf("ateclient: no certificates in %s", o.CAFile)
		}
		cfg.RootCAs = pool
	}
	return cfg, nil
}

// Dial returns a Control client. The connection is lazy; errors surface on the first call.
func Dial(o Options) (ateapipb.ControlClient, io.Closer, error) {
	if o.Addr == "" {
		return nil, nil, errors.New("ateclient: Addr is required")
	}
	tc, err := TLSConfig(o)
	if err != nil {
		return nil, nil, err
	}
	opts := []grpc.DialOption{grpc.WithTransportCredentials(credentials.NewTLS(tc))}
	if o.TokenFile != "" {
		opts = append(opts, grpc.WithPerRPCCredentials(fileToken{path: o.TokenFile}))
	}
	conn, err := grpc.NewClient(o.Addr, opts...)
	if err != nil {
		return nil, nil, fmt.Errorf("ateclient: dial %s: %w", o.Addr, err)
	}
	return ateapipb.NewControlClient(conn), conn, nil
}

// fileToken sends the contents of a token file as a bearer token.
type fileToken struct{ path string }

func (f fileToken) GetRequestMetadata(context.Context, ...string) (map[string]string, error) {
	b, err := os.ReadFile(f.path)
	if err != nil {
		return nil, fmt.Errorf("ateclient: read token: %w", err)
	}
	tok := strings.TrimSpace(string(b))
	if tok == "" {
		return nil, fmt.Errorf("ateclient: token file %s is empty", f.path)
	}
	return map[string]string{"authorization": "Bearer " + tok}, nil
}

func (fileToken) RequireTransportSecurity() bool { return true }
