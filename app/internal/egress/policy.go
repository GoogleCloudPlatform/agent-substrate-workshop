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

// Package egress manages actor EgressPolicies with idempotent create-or-update (apply)
// and --all fan-out across an atespace. Policy files are the EgressPolicy protojson
// shape written as YAML, rules only; the tool fills in metadata.
package egress

import (
	"context"
	"errors"
	"fmt"

	ateapipb "go.ate.dev/substrate/api/ateapi/v1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
	"sigs.k8s.io/yaml"
)

// PolicyName is the one policy an actor has.
const PolicyName = "default"

// ParsePolicy reads a policy file.
func ParsePolicy(data []byte) (*ateapipb.EgressPolicy, error) {
	j, err := yaml.YAMLToJSON(data)
	if err != nil {
		return nil, fmt.Errorf("policy is not valid YAML: %w", err)
	}
	p := &ateapipb.EgressPolicy{}
	if err := protojson.Unmarshal(j, p); err != nil {
		return nil, fmt.Errorf("policy does not match the EgressPolicy schema: %w", err)
	}
	if p.GetMetadata() != nil {
		return nil, errors.New("policy files must not set metadata; egressctl names the policy per actor")
	}
	if len(p.GetRules()) == 0 {
		return nil, errors.New("policy has no rules; to block all egress, delete the policy instead (no policy = no tunnel)")
	}
	return p, nil
}

// Apply creates the actor's policy or, if one exists, replaces its rules.
func Apply(ctx context.Context, c ateapipb.ControlClient, actor *ateapipb.ObjectRef, p *ateapipb.EgressPolicy) (bool, error) {
	want := proto.Clone(p).(*ateapipb.EgressPolicy)
	want.Metadata = &ateapipb.ResourceMetadata{Atespace: actor.GetAtespace(), Name: PolicyName}
	_, err := c.CreateActorEgressPolicy(ctx, &ateapipb.CreateActorEgressPolicyRequest{Actor: actor, EgressPolicy: want})
	if err == nil {
		return true, nil
	}
	if status.Code(err) != codes.AlreadyExists {
		return false, fmt.Errorf("create policy for %s/%s: %w", actor.GetAtespace(), actor.GetName(), err)
	}
	cur, err := c.GetActorEgressPolicy(ctx, &ateapipb.GetActorEgressPolicyRequest{Actor: actor, Name: PolicyName})
	if err != nil {
		return false, fmt.Errorf("get policy for %s/%s: %w", actor.GetAtespace(), actor.GetName(), err)
	}
	want.Metadata = cur.GetMetadata()
	if _, err := c.UpdateActorEgressPolicy(ctx, &ateapipb.UpdateActorEgressPolicyRequest{Actor: actor, EgressPolicy: want}); err != nil {
		return false, fmt.Errorf("update policy for %s/%s: %w", actor.GetAtespace(), actor.GetName(), err)
	}
	return false, nil
}

// Targets resolves --actor NAME or --all into actor refs.
func Targets(ctx context.Context, c ateapipb.ControlClient, atespace, actor string, all bool) ([]*ateapipb.ObjectRef, error) {
	if (actor == "") == !all {
		return nil, errors.New("pass exactly one of --actor NAME or --all")
	}
	if !all {
		return []*ateapipb.ObjectRef{{Atespace: atespace, Name: actor}}, nil
	}
	var refs []*ateapipb.ObjectRef
	token := ""
	for {
		resp, err := c.ListActors(ctx, &ateapipb.ListActorsRequest{
			Atespace: atespace,
			Options:  &ateapipb.ListOptions{PageSize: 100, PageToken: token},
		})
		if err != nil {
			return nil, fmt.Errorf("list actors in %s: %w", atespace, err)
		}
		for _, a := range resp.GetActors() {
			refs = append(refs, &ateapipb.ObjectRef{Atespace: atespace, Name: a.GetMetadata().GetName()})
		}
		if token = resp.GetNextPageToken(); token == "" {
			return refs, nil
		}
	}
}

// Get returns the actor's policy.
func Get(ctx context.Context, c ateapipb.ControlClient, actor *ateapipb.ObjectRef) (*ateapipb.EgressPolicy, error) {
	return c.GetActorEgressPolicy(ctx, &ateapipb.GetActorEgressPolicyRequest{Actor: actor, Name: PolicyName})
}

// Delete removes the actor's policy; a missing policy is not an error.
func Delete(ctx context.Context, c ateapipb.ControlClient, actor *ateapipb.ObjectRef) error {
	_, err := c.DeleteActorEgressPolicy(ctx, &ateapipb.DeleteActorEgressPolicyRequest{Actor: actor, Name: PolicyName})
	if status.Code(err) == codes.NotFound {
		return nil
	}
	return err
}
