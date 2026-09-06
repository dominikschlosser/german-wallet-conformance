package conformance

import (
	"fmt"
	"strings"
)

// DefaultSelection keeps both issuance plans and HAIP redirect presentation.
const DefaultSelection = "vci-,vp-haip-"

type Scenario struct {
	Slug, Kind, Name, Credential string
	Variant                      Variant
	HAIP                         bool
}

func Scenarios(sdjwtConfiguration, only, exclude string) []Scenario {
	var all []Scenario
	kinds := []string{"mdoc"}
	if sdjwtConfiguration != "" {
		kinds = []string{"sdjwt", "mdoc"}
	}
	tokens := map[string]string{"authorization_code": "authcode", "pre_authorization_code": "preauth", "by_value": "byval", "by_reference": "byref"}
	for _, kind := range kinds {
		format := kind
		if kind == "sdjwt" {
			format = "sd_jwt_vc"
		}
		for _, grant := range []string{"authorization_code", "pre_authorization_code"} {
			for _, offer := range []string{"by_value", "by_reference"} {
				for _, encryption := range []string{"plain", "encrypted"} {
					all = append(all, Scenario{Slug: fmt.Sprintf("vci-final-%s-%s-%s-immediate-%s", kind, tokens[grant], tokens[offer], encryption), Kind: "vci", Name: "oid4vci-1_0-wallet-test-plan", Credential: kind, Variant: Variant{
						"client_auth_type": "client_attestation", "fapi_request_method": "unsigned", "sender_constrain": "dpop", "authorization_request_type": "simple", "fapi_profile": "vci", "vci_grant_type": grant, "vci_authorization_code_flow_variant": "issuer_initiated", "vci_credential_offer_variant": offer, "credential_format": format, "vci_credential_issuance_mode": "immediate", "vci_credential_encryption": encryption,
					}})
				}
			}
		}
	}
	for _, kind := range kinds {
		for _, offer := range []string{"by_value", "by_reference"} {
			format := kind
			if kind == "sdjwt" {
				format = "sd_jwt_vc"
			}
			all = append(all, Scenario{Slug: "vci-haip-" + kind + "-" + tokens[offer], Kind: "vci", Name: "oid4vci-1_0-wallet-haip-test-plan", Credential: kind, HAIP: true, Variant: Variant{"vci_authorization_code_flow_variant": "issuer_initiated", "vci_credential_offer_variant": offer, "credential_format": format}})
		}
	}
	for _, kind := range []string{"sdjwt", "mdoc"} {
		for _, prefix := range []string{"x509_hash", "x509_san_dns"} {
			token, format := "hash", "sd_jwt_vc"
			if prefix == "x509_san_dns" {
				token = "sandns"
			}
			if kind == "mdoc" {
				format = "iso_mdl"
			}
			all = append(all, Scenario{Slug: "vp-final-" + kind + "-" + token + "-signed-direct-post-jwt", Kind: "vp", Name: "oid4vp-1final-wallet-test-plan", Credential: kind, Variant: Variant{"vp_profile": "plain_vp", "credential_format": format, "client_id_prefix": prefix, "request_method": "request_uri_signed", "response_mode": "direct_post.jwt"}})
		}
	}
	for _, kind := range []string{"sdjwt", "mdoc"} {
		format := "sd_jwt_vc"
		if kind == "mdoc" {
			format = "iso_mdl"
		}
		all = append(all, Scenario{Slug: "vp-haip-" + kind + "-direct-post-jwt", Kind: "vp", Name: "oid4vp-1final-wallet-haip-test-plan", Credential: kind, HAIP: true, Variant: Variant{"credential_format": format, "response_mode": "direct_post.jwt"}})
	}
	// Append deferred Final variants so existing numeric selectors keep their meaning.
	for _, immediate := range all {
		if immediate.Kind != "vci" || immediate.HAIP {
			continue
		}
		deferred := immediate
		deferred.Slug = strings.Replace(immediate.Slug, "-immediate-", "-deferred-", 1)
		deferred.Variant = cloneVariant(immediate.Variant)
		deferred.Variant["vci_credential_issuance_mode"] = "deferred"
		all = append(all, deferred)
	}
	var selected []Scenario
	for _, s := range all {
		if (only == "" || containsAny(s.Slug, only)) && !containsAny(s.Slug, exclude) {
			selected = append(selected, s)
		}
	}
	return selected
}
func ModuleApplies(s Scenario, m Module) bool {
	if s.Kind != "vp" || s.HAIP {
		return true
	}
	// These modules do not apply to the selected signed X.509 direct_post.jwt variants.
	switch m.Name {
	case "oid4vp-1final-wallet-negative-test-response-uri-not-client-id", "oid4vp-1final-wallet-multisigned-one-invalid-signature", "oid4vp-1final-wallet-negative-test-wrong-expected-origins":
		return false
	}
	return true
}
