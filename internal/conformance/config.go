package conformance

import (
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	jose "github.com/go-jose/go-jose/v4"
)

type Config struct {
	Alias          string            `json:"alias"`
	Description    string            `json:"description"`
	Server         ServerConfig      `json:"server"`
	Client         ClientConfig      `json:"client"`
	Client2        *ClientConfig     `json:"client2,omitempty"`
	Credential     CredentialConfig  `json:"credential,omitempty"`
	Attestation    AttestationConfig `json:"client_attestation,omitempty"`
	VCI            VCIConfig         `json:"vci,omitempty"`
	Wait           int               `json:"waitTimeoutSeconds,omitempty"`
	AdditionalWait int               `json:"maxWaitForAdditionalRequestSeconds,omitempty"`
	Browser        []json.RawMessage `json:"browser"`
	Options        json.RawMessage   `json:"options,omitempty"`
}
type ServerConfig struct {
	JWKS                  *jose.JSONWebKeySet `json:"jwks,omitempty"`
	AuthorizationEndpoint string              `json:"authorization_endpoint,omitempty"`
}
type ClientConfig struct {
	ID           string             `json:"client_id,omitempty"`
	Scope        string             `json:"scope,omitempty"`
	RedirectURI  string             `json:"redirect_uri,omitempty"`
	Certificate  string             `json:"certificate,omitempty"`
	JWKS         jose.JSONWebKeySet `json:"jwks"`
	DCQL         *DCQLQuery         `json:"dcql,omitempty"`
	VerifierInfo json.RawMessage    `json:"verifier_info,omitempty"`
	ResponseAlg  string             `json:"authorization_encrypted_response_alg,omitempty"`
	ResponseEnc  string             `json:"authorization_encrypted_response_enc,omitempty"`
}
type DCQLQuery struct {
	Credentials []DCQLCredential `json:"credentials"`
}
type DCQLCredential struct {
	ID     string      `json:"id"`
	Format string      `json:"format"`
	Meta   DCQLMeta    `json:"meta"`
	Claims []DCQLClaim `json:"claims"`
}
type DCQLMeta struct {
	DocType string   `json:"doctype_value,omitempty"`
	VCTs    []string `json:"vct_values,omitempty"`
}
type DCQLClaim struct {
	Path []string `json:"path"`
}
type CredentialConfig struct {
	SigningJWK        *jose.JSONWebKey `json:"signing_jwk,omitempty"`
	TrustAnchor       string           `json:"trust_anchor_pem,omitempty"`
	StatusTrustAnchor string           `json:"status_list_trust_anchor_pem,omitempty"`
}
type AttestationConfig struct {
	Issuer         string              `json:"issuer,omitempty"`
	TrustAnchor    string              `json:"trust_anchor,omitempty"`
	AttesterJWKS   *jose.JSONWebKeySet `json:"attester_jwks,omitempty"`
	KeyJWKS        *jose.JSONWebKeySet `json:"key_attestation_jwks,omitempty"`
	KeyTrustAnchor string              `json:"key_attestation_trust_anchor_pem,omitempty"`
}
type VCIConfig struct {
	OfferEndpoint          string              `json:"credential_offer_endpoint,omitempty"`
	ConfigurationID        string              `json:"credential_configuration_id,omitempty"`
	AttestationIssuer      string              `json:"client_attestation_issuer,omitempty"`
	AttestationTrustAnchor string              `json:"client_attestation_trust_anchor,omitempty"`
	AttesterJWKS           *jose.JSONWebKeySet `json:"client_attester_keys_jwks,omitempty"`
	KeyJWKS                *jose.JSONWebKeySet `json:"key_attestation_jwks,omitempty"`
	KeyTrustAnchor         string              `json:"key_attestation_trust_anchor_pem,omitempty"`
}

var templatePlaceholder = regexp.MustCompile(`\{([A-Za-z0-9._-]+\.json)\}`)

func LoadTemplate(path string) (Config, error) {
	var cfg Config
	raw, err := os.ReadFile(path)
	if err != nil {
		return cfg, err
	}
	var expansionErr error
	raw = templatePlaceholder.ReplaceAllFunc(raw, func(match []byte) []byte {
		name := string(match[1 : len(match)-1])
		data, e := os.ReadFile(filepath.Join(filepath.Dir(filepath.Dir(path)), "certs-keys", name))
		if e != nil {
			expansionErr = e
		}
		return data
	})
	if expansionErr != nil {
		return cfg, expansionErr
	}
	err = json.Unmarshal(raw, &cfg)
	return cfg, err
}
func HolderKey(path string) (jose.JSONWebKey, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return jose.JSONWebKey{}, err
	}
	block, _ := pem.Decode(data)
	if block == nil {
		return jose.JSONWebKey{}, fmt.Errorf("invalid holder PEM")
	}
	key, err := x509.ParseECPrivateKey(block.Bytes)
	if err != nil {
		return jose.JSONWebKey{}, err
	}
	return jose.JSONWebKey{Key: &key.PublicKey, KeyID: "de-wallet-ios-holder"}, nil
}
func PresentationQuery(kind string) *DCQLQuery {
	c := DCQLCredential{ID: "pid", Format: "dc+sd-jwt", Meta: DCQLMeta{VCTs: []string{"urn:eudi:pid:1"}}, Claims: []DCQLClaim{{[]string{"given_name"}}, {[]string{"family_name"}}}}
	if kind == "mdoc" {
		c = DCQLCredential{ID: "mdl", Format: "mso_mdoc", Meta: DCQLMeta{DocType: "org.iso.18013.5.1.mDL"}, Claims: []DCQLClaim{{[]string{"org.iso.18013.5.1", "given_name"}}, {[]string{"org.iso.18013.5.1", "family_name"}}}}
	}
	return &DCQLQuery{Credentials: []DCQLCredential{c}}
}
func WriteConfig(path string, cfg Config) error {
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(data, '\n'), 0600)
}
func templateName(s Scenario) string {
	if s.Kind == "vp" {
		return "vp-wallet-test-config-dcql-" + s.Credential + ".json"
	}
	if s.HAIP {
		return "vci-wallet-test-config-haip.json"
	}
	return "vci-wallet-test-config-plain.json"
}
func templatePath(suite, name string) string {
	return filepath.Join(suite, "scripts", "test-configs-rp-against-op", name)
}
func cloneVariant(v Variant) Variant {
	copy := make(Variant, len(v))
	for k, val := range v {
		copy[k] = val
	}
	return copy
}
func containsAny(value, patterns string) bool {
	for _, p := range strings.Split(patterns, ",") {
		if p != "" && strings.Contains(value, p) {
			return true
		}
	}
	return false
}

type ConfigOptions struct {
	SuiteDir, AliasSuffix, VCIAlias, RedirectURI, OfferEndpoint, BackendURL, SuiteHost, Description, SDJWTConfiguration string
	Holder                                                                                                              jose.JSONWebKey
	SuiteURL, BackendCA                                                                                                 string
}

func BuildConfig(s Scenario, o ConfigOptions) (Config, error) {
	cfg, err := LoadTemplate(templatePath(o.SuiteDir, templateName(s)))
	if err != nil {
		return cfg, err
	}
	cfg.Description = o.Description
	if s.Kind == "vci" {
		cfg.Alias = o.VCIAlias
		cfg.Wait = 10
		cfg.AdditionalWait = 20
		cfg.Client.ID = "local-german-wallet"
		cfg.Client.RedirectURI = o.RedirectURI
		keys := &jose.JSONWebKeySet{Keys: []jose.JSONWebKey{o.Holder}}
		cfg.Client.JWKS = *keys
		cfg.VCI = VCIConfig{OfferEndpoint: o.OfferEndpoint, ConfigurationID: o.SDJWTConfiguration, AttestationIssuer: o.BackendURL + "/v1/wpb", AttestationTrustAnchor: o.BackendCA, AttesterJWKS: keys, KeyJWKS: keys, KeyTrustAnchor: o.BackendCA}
		if s.Credential == "mdoc" {
			cfg.VCI.ConfigurationID = "org.iso.18013.5.1.mDL"
		}
		if cfg.VCI.ConfigurationID == "" {
			cfg.VCI.ConfigurationID = "eu.europa.ec.eudi.pid.1"
		}
		cfg.Attestation = AttestationConfig{Issuer: cfg.VCI.AttestationIssuer, TrustAnchor: o.BackendCA, AttesterJWKS: keys, KeyJWKS: keys, KeyTrustAnchor: o.BackendCA}
		cfg.Browser = []json.RawMessage{}
	} else {
		cfg.Alias = "german-wallet-" + s.Slug + "-" + o.AliasSuffix
		cfg.Server.AuthorizationEndpoint = strings.ReplaceAll(cfg.Server.AuthorizationEndpoint, "{BASEURL}", strings.TrimSuffix(o.SuiteURL, "/")+"/")
		cfg.Client.DCQL = PresentationQuery(s.Credential)
		if s.HAIP || s.Variant["client_id_prefix"] == "x509_san_dns" {
			cfg.Client.ID = o.SuiteHost
		}
		cfg.Client.ResponseAlg = "ECDH-ES"
		cfg.Client.ResponseEnc = "A128GCM"
		if s.HAIP {
			second := ClientConfig{ID: cfg.Client.ID, JWKS: jose.JSONWebKeySet{Keys: append([]jose.JSONWebKey(nil), cfg.Client.JWKS.Keys...)}}
			if len(second.JWKS.Keys) > 0 {
				second.JWKS.Keys[0].KeyID += "-second"
			}
			cfg.Client2 = &second
			data, e := os.ReadFile(filepath.Join(o.SuiteDir, "scripts/certs-keys/vci-test-root.crt"))
			if e != nil {
				return cfg, e
			}
			anchor := string(data)
			cfg.Credential.TrustAnchor = anchor
			cfg.Credential.StatusTrustAnchor = anchor
		}
	}
	return cfg, nil
}
