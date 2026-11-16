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

// Package ateclient dials the Agent Substrate control plane (ateapi) over TLS
// with rotating service account tokens, builds mTLS client configurations for
// atenet-router, and ensures Incident Actors carry default-deny egress policies.
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
	"time"

	ateapipb "go.ate.dev/substrate/api/ateapi/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
)

const (
	// DefaultServerName is the DNS SAN on ateapi's serving certificate.
	DefaultServerName = "api.ate-system.svc"
	// DefaultEgressPolicyName is the singleton per-actor egress policy name in Substrate.
	DefaultEgressPolicyName = "default"
)

// Options configures TLS trust, rotating client certificates, and bearer tokens
// for ateapi or atenet-router connections.
type Options struct {
	Addr           string
	ServerName     string
	CAFile         string
	TokenFile      string
	ClientCertFile string
	Insecure       bool
}

// TLSConfig builds a client TLS configuration that reloads ClientCertFile on
// every handshake so kubelet-rotated podCertificates are picked up automatically.
func TLSConfig(o Options) (*tls.Config, error) {
	name := strings.TrimSpace(o.ServerName)
	if name == "" {
		name = DefaultServerName
	}
	cfg := &tls.Config{
		ServerName: name,
		MinVersion: tls.VersionTLS12,
	}
	if certPath := strings.TrimSpace(o.ClientCertFile); certPath != "" {
		cfg.GetClientCertificate = func(*tls.CertificateRequestInfo) (*tls.Certificate, error) {
			cert, err := tls.LoadX509KeyPair(certPath, certPath)
			if err != nil {
				return nil, fmt.Errorf("ateclient: load client certificate %s: %w", certPath, err)
			}
			return &cert, nil
		}
	}
	if o.Insecure {
		cfg.InsecureSkipVerify = true //nolint:gosec // explicit opt-in for local port-forward debugging
		return cfg, nil
	}
	if caPath := strings.TrimSpace(o.CAFile); caPath != "" {
		pemBytes, err := os.ReadFile(caPath)
		if err != nil {
			return nil, fmt.Errorf("ateclient: read trust bundle %s: %w", caPath, err)
		}
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM(pemBytes) {
			return nil, fmt.Errorf("ateclient: no PEM certificates found in %s", caPath)
		}
		cfg.RootCAs = pool
	}
	return cfg, nil
}

// Dial opens a lazy gRPC ControlClient connection to ateapi.
func Dial(o Options) (ateapipb.ControlClient, io.Closer, error) {
	if strings.TrimSpace(o.Addr) == "" {
		return nil, nil, errors.New("ateclient: Addr is required")
	}
	tc, err := TLSConfig(o)
	if err != nil {
		return nil, nil, err
	}
	dialOpts := []grpc.DialOption{grpc.WithTransportCredentials(credentials.NewTLS(tc))}
	if tokPath := strings.TrimSpace(o.TokenFile); tokPath != "" {
		dialOpts = append(dialOpts, grpc.WithPerRPCCredentials(rotatingFileToken{path: tokPath}))
	}
	conn, err := grpc.NewClient(o.Addr, dialOpts...)
	if err != nil {
		return nil, nil, fmt.Errorf("ateclient: dial %s: %w", o.Addr, err)
	}
	return ateapipb.NewControlClient(conn), conn, nil
}

type rotatingFileToken struct {
	path string
}

func (f rotatingFileToken) GetRequestMetadata(context.Context, ...string) (map[string]string, error) {
	b, err := os.ReadFile(f.path)
	if err != nil {
		return nil, fmt.Errorf("ateclient: read projected token %s: %w", f.path, err)
	}
	tok := strings.TrimSpace(string(b))
	if tok == "" {
		return nil, fmt.Errorf("ateclient: projected token %s is empty", f.path)
	}
	return map[string]string{"authorization": "Bearer " + tok}, nil
}

func (rotatingFileToken) RequireTransportSecurity() bool { return true }

// GatewayOnlyEgressPolicy builds the default-deny EgressPolicy allowing HTTP traffic
// strictly to gatewayHost on ports 80 and 8080.
func GatewayOnlyEgressPolicy(gatewayHost string) *ateapipb.EgressPolicy {
	return &ateapipb.EgressPolicy{
		Rules: []*ateapipb.EgressRule{
			{
				Http: &ateapipb.HttpRule{
					Hostnames: []string{strings.ToLower(strings.TrimSpace(gatewayHost))},
					Ports: &ateapipb.Ports{
						Numbers: []int32{80, 8080},
					},
				},
			},
		},
	}
}

// egressPolicyClient is the narrow ateapi surface used by ApplyEgressPolicy.
type egressPolicyClient interface {
	CreateActorEgressPolicy(ctx context.Context, in *ateapipb.CreateActorEgressPolicyRequest, opts ...grpc.CallOption) (*ateapipb.EgressPolicy, error)
	GetActorEgressPolicy(ctx context.Context, in *ateapipb.GetActorEgressPolicyRequest, opts ...grpc.CallOption) (*ateapipb.EgressPolicy, error)
	UpdateActorEgressPolicy(ctx context.Context, in *ateapipb.UpdateActorEgressPolicyRequest, opts ...grpc.CallOption) (*ateapipb.EgressPolicy, error)
}

// actorEnsurer is the narrow ateapi surface used by EnsureActor.
type actorEnsurer interface {
	egressPolicyClient
	GetActor(ctx context.Context, in *ateapipb.GetActorRequest, opts ...grpc.CallOption) (*ateapipb.Actor, error)
	CreateActor(ctx context.Context, in *ateapipb.CreateActorRequest, opts ...grpc.CallOption) (*ateapipb.Actor, error)
	DeleteActor(ctx context.Context, in *ateapipb.DeleteActorRequest, opts ...grpc.CallOption) (*ateapipb.Actor, error)
}

// actorLister is the narrow ateapi surface used by ListAllActors.
type actorLister interface {
	ListActors(ctx context.Context, in *ateapipb.ListActorsRequest, opts ...grpc.CallOption) (*ateapipb.ListActorsResponse, error)
}

// actorSuspender is the narrow ateapi surface used by SuspendActor.
type actorSuspender interface {
	SuspendActor(ctx context.Context, in *ateapipb.SuspendActorRequest, opts ...grpc.CallOption) (*ateapipb.SuspendActorResponse, error)
}

// ApplyEgressPolicy creates or updates the singleton default EgressPolicy on actor.
func ApplyEgressPolicy(ctx context.Context, c egressPolicyClient, actor *ateapipb.ObjectRef, policy *ateapipb.EgressPolicy) error {
	want := proto.Clone(policy).(*ateapipb.EgressPolicy)
	want.Metadata = &ateapipb.ResourceMetadata{
		Atespace: actor.GetAtespace(),
		Name:     DefaultEgressPolicyName,
	}
	_, err := c.CreateActorEgressPolicy(ctx, &ateapipb.CreateActorEgressPolicyRequest{
		Actor:        actor,
		EgressPolicy: want,
	})
	if err == nil {
		return nil
	}
	if status.Code(err) != codes.AlreadyExists {
		return fmt.Errorf("create egress policy for %s/%s: %w", actor.GetAtespace(), actor.GetName(), err)
	}
	cur, err := c.GetActorEgressPolicy(ctx, &ateapipb.GetActorEgressPolicyRequest{
		Actor: actor,
		Name:  DefaultEgressPolicyName,
	})
	if err != nil {
		return fmt.Errorf("get egress policy for %s/%s: %w", actor.GetAtespace(), actor.GetName(), err)
	}
	want.Metadata = cur.GetMetadata()
	if _, err := c.UpdateActorEgressPolicy(ctx, &ateapipb.UpdateActorEgressPolicyRequest{
		Actor:        actor,
		EgressPolicy: want,
	}); err != nil {
		return fmt.Errorf("update egress policy for %s/%s: %w", actor.GetAtespace(), actor.GetName(), err)
	}
	return nil
}

// EnsureActor gets or creates an Incident Actor in atespace from templateName and
// attaches a default-deny EgressPolicy permitting traffic strictly to gatewayHost.
// When an existing Actor is in ACTOR_STATE_DELETING or references a different
// ActorTemplate, EnsureActor waits for asynchronous deletion to reach NotFound
// before creating the replacement Actor from its Golden Snapshot.
func EnsureActor(ctx context.Context, c actorEnsurer, atespace, name, templateName, gatewayHost string) (*ateapipb.Actor, error) {
	if _, hasDeadline := ctx.Deadline(); !hasDeadline {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, 60*time.Second)
		defer cancel()
	}
	ref := &ateapipb.ObjectRef{Atespace: atespace, Name: name}
	wantTemplate := strings.TrimSpace(templateName)

	for {
		actor, err := c.GetActor(ctx, &ateapipb.GetActorRequest{
			Actor: ref,
		})
		if err == nil {
			if actor.GetStatus().GetState() == ateapipb.ActorState_ACTOR_STATE_DELETING {
				if waitErr := waitActorDeleted(ctx, c, ref); waitErr != nil {
					return nil, fmt.Errorf("wait for deleting actor %s/%s: %w", atespace, name, waitErr)
				}
				err = status.Error(codes.NotFound, "actor finished deleting")
			} else if wantTemplate != "" && actor.GetActorTemplate().GetName() != "" && actor.GetActorTemplate().GetName() != wantTemplate {
				if _, delErr := c.DeleteActor(ctx, &ateapipb.DeleteActorRequest{Actor: ref}); delErr != nil && status.Code(delErr) != codes.NotFound {
					return nil, fmt.Errorf("replace actor %s/%s template %s -> %s: %w", atespace, name, actor.GetActorTemplate().GetName(), wantTemplate, delErr)
				}
				if waitErr := waitActorDeleted(ctx, c, ref); waitErr != nil {
					return nil, fmt.Errorf("wait for replaced actor %s/%s deletion: %w", atespace, name, waitErr)
				}
				err = status.Error(codes.NotFound, "recreating actor for upgraded template")
			}
		}
		if status.Code(err) == codes.NotFound {
			actor, err = c.CreateActor(ctx, &ateapipb.CreateActorRequest{
				Actor: &ateapipb.Actor{
					Metadata: &ateapipb.ResourceMetadata{
						Atespace: atespace,
						Name:     name,
					},
					ActorTemplate: &ateapipb.ObjectRef{
						Atespace: atespace,
						Name:     wantTemplate,
					},
				},
			})
			if status.Code(err) == codes.AlreadyExists {
				if sleepErr := sleepContext(ctx, 25*time.Millisecond); sleepErr != nil {
					return nil, fmt.Errorf("ensure actor %s/%s: %w", atespace, name, sleepErr)
				}
				continue
			}
		}
		if err != nil {
			return nil, fmt.Errorf("ensure actor %s/%s: %w", atespace, name, err)
		}
		if strings.TrimSpace(gatewayHost) != "" {
			if err := ApplyEgressPolicy(ctx, c, ref, GatewayOnlyEgressPolicy(gatewayHost)); err != nil {
				return nil, err
			}
		}
		return actor, nil
	}
}

func waitActorDeleted(ctx context.Context, c actorEnsurer, ref *ateapipb.ObjectRef) error {
	delay := 25 * time.Millisecond
	for {
		if err := sleepContext(ctx, delay); err != nil {
			return err
		}
		_, err := c.GetActor(ctx, &ateapipb.GetActorRequest{Actor: ref})
		if status.Code(err) == codes.NotFound {
			return nil
		}
		if err != nil {
			return err
		}
		if delay < 250*time.Millisecond {
			delay *= 2
			if delay > 250*time.Millisecond {
				delay = 250 * time.Millisecond
			}
		}
	}
}

func sleepContext(ctx context.Context, d time.Duration) error {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

// ListAllActors lists every Actor in atespace across pages.
func ListAllActors(ctx context.Context, c actorLister, atespace string) ([]*ateapipb.Actor, error) {
	var out []*ateapipb.Actor
	pageToken := ""
	for {
		resp, err := c.ListActors(ctx, &ateapipb.ListActorsRequest{
			Atespace: atespace,
			Options: &ateapipb.ListOptions{
				PageSize:  100,
				PageToken: pageToken,
			},
		})
		if err != nil {
			return nil, err
		}
		out = append(out, resp.GetActors()...)
		pageToken = resp.GetNextPageToken()
		if pageToken == "" {
			return out, nil
		}
	}
}

// SuspendActor transitions an Incident Actor to ACTOR_STATE_SUSPENDED in Cloud Storage.
func SuspendActor(ctx context.Context, c actorSuspender, atespace, name string) error {
	_, err := c.SuspendActor(ctx, &ateapipb.SuspendActorRequest{
		Actor: &ateapipb.ObjectRef{
			Atespace: atespace,
			Name:     name,
		},
	})
	return err
}
