package auth

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
)

// AuthConfig is the optional JSON file passed to `serve --auth`. An absent
// file means local accounts only; an `oidc` section enables single sign-on.
type AuthConfig struct {
	OIDC *OIDCConfig `json:"oidc,omitempty"`
}

// LoadAuthConfig reads an auth configuration file. OIDC defaults and URL
// checks are applied later by Validate, once the listener address is known.
func LoadAuthConfig(path string) (AuthConfig, error) {
	var config AuthConfig
	f, err := os.Open(path)
	if err != nil {
		return config, err
	}
	defer f.Close()
	d := json.NewDecoder(io.LimitReader(f, 65537))
	d.DisallowUnknownFields()
	if err := d.Decode(&config); err != nil {
		return config, fmt.Errorf("auth configuration: %w", err)
	}
	var extra any
	if err := d.Decode(&extra); err != io.EOF {
		return config, fmt.Errorf("auth configuration must contain one JSON object")
	}
	return config, nil
}

// Validate applies defaults and checks the optional OIDC section using the
// listener's base URL (for example http://127.0.0.1:8080) as the default
// redirect origin.
func (c AuthConfig) Validate(redirectBase string) (AuthConfig, error) {
	if c.OIDC == nil {
		return c, nil
	}
	normalized, err := c.OIDC.Validate(redirectBase)
	if err != nil {
		return c, err
	}
	c.OIDC = &normalized
	return c, nil
}
