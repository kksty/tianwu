package settings

import "testing"

// Endpoint labels are independent of legacy monetary metadata.
func TestLocalEndpointLabelsIgnoreLegacyPricing(t *testing.T) {
	s, _ := loadProfiles(t, `llm:
    active_profile: local
    providers:
        - id: local
          provider: openai
          base_url: http://192.168.1.10:8000/v1
          api_key: k
          model: local-model
          pricing:
              currency: USD
              input_per_mtoken: 2
              output_per_mtoken: 10
              cached_input_per_mtoken: 0.5
        - id: paid
          provider: openai
          base_url: https://api.example.com/v1
          api_key: k
          model: paid-model
          pricing:
              currency: USD
              input_per_mtoken: 2
              output_per_mtoken: 10
              cached_input_per_mtoken: 0.5
`)
	cat, err := s.Profiles()
	if err != nil {
		t.Fatal(err)
	}
	byID := map[string]ProfileView{}
	for _, p := range cat.Profiles {
		byID[p.ID] = p
	}
	if got := byID["local"]; !got.Local {
		t.Fatalf("local profile = %+v, want Local=true", got)
	}
	if got := byID["paid"]; got.Local {
		t.Fatalf("paid profile = %+v, want Local=false", got)
	}
}
