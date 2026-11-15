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

package broker

import (
	"crypto/x509"
	"encoding/pem"
	"net/url"
	"os"
	"strings"
	"time"
)

const (
	// DefaultAgentIdentityCertFile is the kubelet-mounted Pod Agent Identity X.509
	// SVID credential bundle path on GKE 1.37+ when iam.gke.io/inject-podcertificates="true".
	DefaultAgentIdentityCertFile = "/var/run/secrets/workload-spiffe-credentials/x509.credential-bundle.private-key.pem"
	// DefaultAtenetCertFile is the Substrate podCertificate bundle path used to dial atenet-router.
	DefaultAtenetCertFile = "/var/run/ate/identity/credential-bundle.pem"
)

// IdentityStatus describes the active workload identity and cluster mTLS posture
// observed on sre-gateway.
type IdentityStatus struct {
	Mode              string `json:"mode"`
	TrustDomain       string `json:"trust_domain"`
	Principal         string `json:"principal"`
	SPIFFEID          string `json:"spiffe_id"`
	SVIDMounted       bool   `json:"svid_mounted"`
	SVIDValid         bool   `json:"svid_valid"`
	SVIDExpiresAt     string `json:"svid_expires_at,omitempty"`
	SVIDSerial        string `json:"svid_serial,omitempty"`
	AtenetCertMounted bool   `json:"atenet_cert_mounted"`
	AtenetCertPresent bool   `json:"atenet_cert_present"`
	AtenetSPIFFEID    string `json:"atenet_spiffe_id,omitempty"`
	AtenetExpiresAt   string `json:"atenet_expires_at,omitempty"`
}

type identityInspector struct {
	namespace        string
	configuredMode   string
	configuredDomain string
	agentSVIDFile    string
	atenetBundleFile string
}

func newIdentityInspector(cfg Config) *identityInspector {
	agentFile := strings.TrimSpace(cfg.AgentIdentityCertFile)
	if agentFile == "" {
		agentFile = DefaultAgentIdentityCertFile
	}
	atenetFile := strings.TrimSpace(cfg.RouterClientCert)
	if atenetFile == "" {
		atenetFile = DefaultAtenetCertFile
	}
	return &identityInspector{
		namespace:        firstNonEmpty(cfg.Namespace, "workshop-301"),
		configuredMode:   firstNonEmpty(cfg.SPIFFEMode, "wif"),
		configuredDomain: firstNonEmpty(cfg.SPIFFEDomain, "cluster.local"),
		agentSVIDFile:    agentFile,
		atenetBundleFile: atenetFile,
	}
}

func (i *identityInspector) Inspect() IdentityStatus {
	now := time.Now().UTC()
	fallbackPrincipal := "spiffe://" + i.configuredDomain + "/ns/" + i.namespace + "/sa/sre-gateway"
	st := IdentityStatus{
		Mode:        i.configuredMode,
		TrustDomain: i.configuredDomain,
		Principal:   fallbackPrincipal,
	}

	if svid, ok := parseSPIFFELeafCert(i.agentSVIDFile); ok {
		st.SVIDMounted = true
		st.SVIDValid = !now.Before(svid.notBefore) && now.Before(svid.notAfter)
		st.SVIDExpiresAt = svid.notAfter.UTC().Format(time.RFC3339)
		st.SVIDSerial = svid.serial
		if svid.spiffeURI != "" {
			st.Principal = svid.spiffeURI
		}
		if svid.trustDomain != "" {
			st.TrustDomain = svid.trustDomain
			st.Mode = inferIdentityModeFromDomain(svid.trustDomain, st.Mode)
		}
	} else {
		st.Mode = inferIdentityModeFromDomain(st.TrustDomain, st.Mode)
	}

	if ateCert, ok := parseSPIFFELeafCert(i.atenetBundleFile); ok {
		st.AtenetCertMounted = true
		st.AtenetCertPresent = true
		st.AtenetSPIFFEID = ateCert.spiffeURI
		st.AtenetExpiresAt = ateCert.notAfter.UTC().Format(time.RFC3339)
	}
	st.SPIFFEID = st.Principal
	return st
}

func (i *identityInspector) OutboundPrincipal() string {
	return i.Inspect().Principal
}

type parsedSPIFFECert struct {
	spiffeURI   string
	trustDomain string
	serial      string
	notBefore   time.Time
	notAfter    time.Time
}

func parseSPIFFELeafCert(path string) (parsedSPIFFECert, bool) {
	if strings.TrimSpace(path) == "" {
		return parsedSPIFFECert{}, false
	}
	raw, err := os.ReadFile(path)
	if err != nil || len(raw) == 0 {
		return parsedSPIFFECert{}, false
	}

	var certs []*x509.Certificate
	rest := raw
	for len(rest) > 0 {
		var blk *pem.Block
		blk, rest = pem.Decode(rest)
		if blk == nil {
			break
		}
		if blk.Type != "CERTIFICATE" {
			continue
		}
		c, err := x509.ParseCertificate(blk.Bytes)
		if err == nil {
			certs = append(certs, c)
		}
	}
	if len(certs) == 0 {
		return parsedSPIFFECert{}, false
	}

	leaf := certs[0]
	for _, c := range certs {
		if !c.IsCA {
			leaf = c
			break
		}
	}

	out := parsedSPIFFECert{
		notBefore: leaf.NotBefore,
		notAfter:  leaf.NotAfter,
	}
	if leaf.SerialNumber != nil {
		out.serial = leaf.SerialNumber.Text(16)
	}
	for _, u := range leaf.URIs {
		if u != nil && strings.EqualFold(u.Scheme, "spiffe") {
			out.spiffeURI = u.String()
			out.trustDomain = strings.ToLower(strings.TrimSpace(u.Host))
			break
		}
	}
	if out.trustDomain == "" && out.spiffeURI != "" {
		if u, err := url.Parse(out.spiffeURI); err == nil {
			out.trustDomain = strings.ToLower(strings.TrimSpace(u.Host))
		}
	}
	return out, true
}

func inferIdentityModeFromDomain(domain, fallback string) string {
	d := strings.ToLower(strings.TrimSpace(domain))
	switch {
	case strings.HasPrefix(d, "agents.global.org-"):
		return "org"
	case strings.HasPrefix(d, "agents.global.proj-"):
		return "project"
	case strings.HasSuffix(d, ".svc.id.goog"):
		return "wif"
	default:
		return firstNonEmpty(fallback, "wif")
	}
}
