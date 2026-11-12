package agent

import (
	"encoding/json"
	"fmt"
	"net"
	"net/url"
	"regexp"
	"strconv"
	"strings"
)

const (
	maxSandboxEnvVars   = 100
	maxEgressPolicyRefs = 16
	maxEgressTargets    = 64
	reservedSandboxName = "triagelab-python"
)

var (
	envVarName    = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]{0,127}$`)
	headerToken   = regexp.MustCompile(`^[!#$%&'*+.^_|~A-Za-z0-9-]{1,128}$`)
	snapshotRoute = regexp.MustCompile(`^[a-z][a-z0-9+.-]*://\S{1,509}$`)
	// The egress gateway injects authentication headers; hop-by-hop and
	// cookie headers are managed by the gateway itself upstream.
	restrictedHeaders = map[string]bool{
		"connection": true, "keep-alive": true, "proxy-authenticate": true, "proxy-authorization": true,
		"te": true, "trailer": true, "transfer-encoding": true, "upgrade": true,
		"cookie": true, "set-cookie": true,
	}
)

type SandboxTemplateDefinition struct {
	Name             string       `json:"name"`
	WorkloadImage    string       `json:"workload_image"`
	Env              []SandboxEnv `json:"env,omitempty"`
	WorkerPool       string       `json:"worker_pool"`
	SnapshotLocation string       `json:"snapshot_location"`
	EgressPolicyRefs []string     `json:"egress_policy_refs,omitempty"`
}

type SandboxEnv struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

type EgressPolicyDefinition struct {
	Name         string              `json:"name"`
	Destinations []EgressDestination `json:"destinations"`
}

type EgressDestination struct {
	Origin     string `json:"origin"`
	Header     string `json:"header,omitempty"`
	Prefix     string `json:"prefix,omitempty"`
	SecretName string `json:"secret_name,omitempty"`
	SecretKey  string `json:"secret_key,omitempty"`
}

// RenderSandboxTemplateDefinition emits a native SandboxTemplate with an
// immutable digest-pinned workload image. It performs no cluster operations.
func RenderSandboxTemplateDefinition(namespace string, d SandboxTemplateDefinition) ([]byte, error) {
	if err := ValidNamespace(namespace); err != nil {
		return nil, err
	}
	if !ValidResourceName(d.Name) || d.Name == reservedSandboxName {
		return nil, fmt.Errorf("sandbox template needs a valid, unreserved Kubernetes resource name")
	}
	spec, err := d.spec()
	if err != nil {
		return nil, err
	}
	return json.MarshalIndent(resource(namespace, "SandboxTemplate", d.Name, spec), "", "  ")
}

// RenderEgressPolicyDefinition emits a native EgressPolicy granting additional
// HTTP(S) origins. Substrate still denies every other destination, including DNS.
func RenderEgressPolicyDefinition(namespace string, d EgressPolicyDefinition) ([]byte, error) {
	if err := ValidNamespace(namespace); err != nil {
		return nil, err
	}
	if !ValidResourceName(d.Name) {
		return nil, fmt.Errorf("egress policy needs a valid Kubernetes resource name")
	}
	if len(d.Destinations) == 0 || len(d.Destinations) > maxEgressTargets {
		return nil, fmt.Errorf("grant between 1 and %d destinations", maxEgressTargets)
	}
	origins := map[string]bool{}
	destinations := make([]map[string]any, 0, len(d.Destinations))
	for _, destination := range d.Destinations {
		if err := validOrigin(destination.Origin); err != nil {
			return nil, err
		}
		if origins[destination.Origin] {
			return nil, fmt.Errorf("grant each destination origin once")
		}
		origins[destination.Origin] = true
		item := map[string]any{"origin": destination.Origin}
		if destination.Header != "" {
			if !headerToken.MatchString(destination.Header) || restrictedHeaders[strings.ToLower(destination.Header)] {
				return nil, fmt.Errorf("egress authentication header must be a valid, non-restricted header name")
			}
			if len(destination.Prefix) > 256 || strings.ContainsRune(destination.Prefix, 0) {
				return nil, fmt.Errorf("egress authentication prefix must be at most 256 bytes without NUL")
			}
			if err := validateSecretRef(destination.SecretName, destination.SecretKey); err != nil {
				return nil, fmt.Errorf("egress authentication: %w", err)
			}
			authentication := map[string]any{
				"header":       destination.Header,
				"secretKeyRef": map[string]string{"name": destination.SecretName, "key": destination.SecretKey},
			}
			if destination.Prefix != "" {
				authentication["prefix"] = destination.Prefix
			}
			item["authentication"] = authentication
		} else if destination.Prefix != "" || destination.SecretName != "" || destination.SecretKey != "" {
			return nil, fmt.Errorf("egress authentication needs a header name")
		}
		destinations = append(destinations, item)
	}
	spec := map[string]any{"destinations": destinations}
	return json.MarshalIndent(resource(namespace, "EgressPolicy", d.Name, spec), "", "  ")
}

func (d SandboxTemplateDefinition) spec() (map[string]any, error) {
	if !digestImage.MatchString(d.WorkloadImage) || len(d.WorkloadImage) > 512 {
		return nil, fmt.Errorf("workload image must be pinned by SHA-256 digest")
	}
	if !ValidResourceName(d.WorkerPool) {
		return nil, fmt.Errorf("worker pool must have a valid Kubernetes resource name")
	}
	if !snapshotRoute.MatchString(d.SnapshotLocation) {
		return nil, fmt.Errorf("snapshot location must be a storage URL such as gs://bucket/path")
	}
	if len(d.Env) > maxSandboxEnvVars {
		return nil, fmt.Errorf("set at most %d environment variables", maxSandboxEnvVars)
	}
	names := map[string]bool{}
	vars := make([]map[string]string, 0, len(d.Env))
	for _, variable := range d.Env {
		if !envVarName.MatchString(variable.Name) {
			return nil, fmt.Errorf("environment names must be valid identifiers such as LANG or MAX_RETRIES")
		}
		if names[variable.Name] {
			return nil, fmt.Errorf("set each environment name once")
		}
		if len(variable.Value) > 4096 || strings.ContainsRune(variable.Value, 0) {
			return nil, fmt.Errorf("environment values must be at most 4096 bytes without NUL")
		}
		names[variable.Name] = true
		vars = append(vars, map[string]string{"name": variable.Name, "value": variable.Value})
	}
	if len(d.EgressPolicyRefs) > maxEgressPolicyRefs {
		return nil, fmt.Errorf("reference at most %d egress policies", maxEgressPolicyRefs)
	}
	refs := map[string]bool{}
	var policyRefs []map[string]string
	for _, name := range d.EgressPolicyRefs {
		if !ValidResourceName(name) || refs[name] {
			return nil, fmt.Errorf("egress policy references must be valid, unique Kubernetes resource names")
		}
		refs[name] = true
		policyRefs = append(policyRefs, map[string]string{"name": name})
	}
	spec := map[string]any{
		"workload": map[string]string{"image": d.WorkloadImage},
		"substrate": map[string]any{
			"workerPoolRef":  map[string]string{"name": d.WorkerPool},
			"snapshotPolicy": map[string]string{"location": d.SnapshotLocation},
		},
	}
	if len(vars) > 0 {
		spec["env"] = vars
	}
	if len(policyRefs) > 0 {
		spec["egressPolicyRefs"] = policyRefs
	}
	return spec, nil
}

// validOrigin accepts a scheme, DNS hostname or IP with an optional port, and
// at most one leftmost wildcard label, matching the pinned EgressPolicy CRD.
func validOrigin(origin string) error {
	if len(origin) > 2048 {
		return fmt.Errorf("destination origins must be at most 2048 bytes")
	}
	parsed, err := url.Parse(origin)
	if err != nil || (parsed.Scheme != "https" && parsed.Scheme != "http") {
		return fmt.Errorf("destination origins must start with https:// or http://")
	}
	if parsed.User != nil || parsed.Path != "" || parsed.RawQuery != "" || parsed.Fragment != "" || parsed.RawFragment != "" {
		return fmt.Errorf("destination origins carry only a scheme, host, and optional port")
	}
	if port := parsed.Port(); port != "" {
		number, err := strconv.Atoi(port)
		if err != nil || number < 1 || number > 65535 {
			return fmt.Errorf("destination origin ports must be between 1 and 65535")
		}
	}
	host := parsed.Hostname()
	if host == "*" {
		return fmt.Errorf("wildcard destinations must name a domain such as https://*.example.com")
	}
	if strings.HasPrefix(host, "*.") {
		host = strings.TrimPrefix(host, "*.")
	}
	if strings.Contains(host, "*") {
		return fmt.Errorf("a wildcard may replace only the leftmost domain label")
	}
	if net.ParseIP(host) != nil {
		return nil
	}
	if host == "" || len(host) > 253 {
		return fmt.Errorf("destination origins need a DNS hostname or address")
	}
	for _, label := range strings.Split(host, ".") {
		if len(label) == 0 || len(label) > 63 || !dnsLabel.MatchString(label) {
			return fmt.Errorf("destination origin hosts must be DNS names or addresses")
		}
	}
	return nil
}
