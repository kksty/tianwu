package access

import (
	"encoding/json"
	"strings"
)

// SecretValues reads explicit credential-bearing fields in the input formats.
// Metadata is never inferred to be a secret and model prose is never parsed.
func SecretValues(cases string) []string {
	rows, err := List(cases)
	if err != nil {
		return nil
	}
	var values []string
	for _, row := range rows {
		raw, err := Material(cases, row.ID)
		if err != nil {
			continue
		}
		switch row.Kind {
		case "state":
			var state struct {
				Cookies []struct {
					Value string `json:"value"`
				} `json:"cookies"`
				Origins []struct {
					LocalStorage []struct {
						Value string `json:"value"`
					} `json:"localStorage"`
				} `json:"origins"`
			}
			if json.Unmarshal(raw, &state) != nil {
				continue
			}
			for _, cookie := range state.Cookies {
				values = append(values, cookie.Value)
			}
			for _, origin := range state.Origins {
				for _, item := range origin.LocalStorage {
					values = append(values, item.Value)
				}
			}
		case "request":
			var request struct {
				Headers map[string]string `json:"headers"`
			}
			if json.Unmarshal(raw, &request) != nil {
				continue
			}
			for name, value := range request.Headers {
				switch strings.ToLower(name) {
				case "authorization", "proxy-authorization", "x-api-key":
					values = append(values, value)
					if _, token, found := strings.Cut(value, " "); found {
						values = append(values, strings.TrimSpace(token))
					}
				case "cookie":
					values = append(values, value)
					for _, part := range strings.Split(value, ";") {
						if _, v, ok := strings.Cut(part, "="); ok {
							values = append(values, strings.TrimSpace(v))
						}
					}
				}
			}
		}
	}
	return values
}
