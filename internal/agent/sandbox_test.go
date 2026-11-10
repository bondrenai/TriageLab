package agent

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

func TestSandboxTemplateRendersTheNativeSpec(t *testing.T) {
	data, err := RenderSandboxTemplateDefinition("kagent", SandboxTemplateDefinition{
		Name:             "network-lab",
		WorkloadImage:    "docker.io/library/python@sha256:" + strings.Repeat("a", 64),
		WorkerPool:       "kagent-default",
		SnapshotLocation: "gs://ate-snapshots/triagelab/",
		Env:              []SandboxEnv{{Name: "MAX_RETRIES", Value: "3"}},
		EgressPolicyRefs: []string{"pypi-egress"},
	})
	if err != nil {
		t.Fatal(err)
	}
	var object struct {
		Kind     string
		Metadata map[string]string
		Spec     map[string]any
	}
	if err := json.Unmarshal(data, &object); err != nil {
		t.Fatal(err)
	}
	env := object.Spec["env"].([]any)[0].(map[string]any)
	ref := object.Spec["egressPolicyRefs"].([]any)[0].(map[string]any)
	substrate := object.Spec["substrate"].(map[string]any)
	if object.Kind != "SandboxTemplate" || object.Metadata["namespace"] != "kagent" ||
		object.Spec["workload"].(map[string]any)["image"] != "docker.io/library/python@sha256:"+strings.Repeat("a", 64) ||
		env["name"] != "MAX_RETRIES" || env["value"] != "3" ||
		substrate["workerPoolRef"].(map[string]any)["name"] != "kagent-default" ||
		substrate["snapshotPolicy"].(map[string]any)["location"] != "gs://ate-snapshots/triagelab/" ||
		ref["name"] != "pypi-egress" {
		t.Fatalf("unexpected sandbox template: %s", data)
	}
}

func TestSandboxTemplateWithoutEgressOmitsTheField(t *testing.T) {
	data, err := RenderSandboxTemplateDefinition("kagent", SandboxTemplateDefinition{
		Name: "plain", WorkloadImage: "docker.io/library/python@sha256:" + strings.Repeat("b", 64),
		WorkerPool: "kagent-default", SnapshotLocation: "gs://ate-snapshots/triagelab/",
	})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "egressPolicyRefs") {
		t.Fatalf("default templates must not grant egress: %s", data)
	}
}

func TestSandboxTemplateRejectsInvalidConfiguration(t *testing.T) {
	digest := "docker.io/library/python@sha256:" + strings.Repeat("a", 64)
	for name, change := range map[string]func(*SandboxTemplateDefinition){
		"reserved name":    func(d *SandboxTemplateDefinition) { d.Name = "triagelab-python" },
		"invalid name":     func(d *SandboxTemplateDefinition) { d.Name = "Not A Name" },
		"tag image":        func(d *SandboxTemplateDefinition) { d.WorkloadImage = "python:3.12" },
		"invalid env name": func(d *SandboxTemplateDefinition) { d.Env = []SandboxEnv{{Name: "3MAX", Value: "1"}} },
		"duplicate env": func(d *SandboxTemplateDefinition) {
			d.Env = []SandboxEnv{{Name: "LANG", Value: "C"}, {Name: "LANG", Value: "C.UTF-8"}}
		},
		"NUL env value": func(d *SandboxTemplateDefinition) { d.Env = []SandboxEnv{{Name: "LANG", Value: "C\x00"}} },
		"too many refs": func(d *SandboxTemplateDefinition) {
			d.EgressPolicyRefs = make([]string, 17)
			for i := range d.EgressPolicyRefs {
				d.EgressPolicyRefs[i] = fmt.Sprintf("egress-%02d", i)
			}
		},
		"duplicate ref":   func(d *SandboxTemplateDefinition) { d.EgressPolicyRefs = []string{"egress", "egress"} },
		"invalid ref":     func(d *SandboxTemplateDefinition) { d.EgressPolicyRefs = []string{"Not A Ref"} },
		"invalid pool":    func(d *SandboxTemplateDefinition) { d.WorkerPool = "kagent Default" },
		"bare location":   func(d *SandboxTemplateDefinition) { d.SnapshotLocation = "ate-snapshots/triagelab" },
		"spaced location": func(d *SandboxTemplateDefinition) { d.SnapshotLocation = "gs://ate-snapshots/triagelab/ extra" },
	} {
		t.Run(name, func(t *testing.T) {
			d := SandboxTemplateDefinition{Name: "network-lab", WorkloadImage: digest,
				WorkerPool: "kagent-default", SnapshotLocation: "gs://ate-snapshots/triagelab/"}
			change(&d)
			if _, err := RenderSandboxTemplateDefinition("kagent", d); err == nil {
				t.Fatal("invalid sandbox template accepted")
			}
		})
	}
}

func TestEgressPolicyRendersNativeDestinations(t *testing.T) {
	data, err := RenderEgressPolicyDefinition("kagent", EgressPolicyDefinition{
		Name: "pypi-egress",
		Destinations: []EgressDestination{
			{Origin: "https://pypi.org"},
			{Origin: "https://*.example.com:8443", Header: "Authorization", Prefix: "Bearer ", SecretName: "egress-token", SecretKey: "value"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	var object struct {
		Kind string
		Spec struct {
			Destinations []struct {
				Origin         string
				Authentication *struct {
					Header       string
					Prefix       string
					SecretKeyRef struct {
						Name string
						Key  string
					}
				}
			}
		}
	}
	if err := json.Unmarshal(data, &object); err != nil {
		t.Fatal(err)
	}
	if object.Kind != "EgressPolicy" || len(object.Spec.Destinations) != 2 ||
		object.Spec.Destinations[0].Origin != "https://pypi.org" || object.Spec.Destinations[0].Authentication != nil {
		t.Fatalf("unexpected plain destination: %s", data)
	}
	auth := object.Spec.Destinations[1].Authentication
	if object.Spec.Destinations[1].Origin != "https://*.example.com:8443" || auth == nil ||
		auth.Header != "Authorization" || auth.Prefix != "Bearer " ||
		auth.SecretKeyRef.Name != "egress-token" || auth.SecretKeyRef.Key != "value" {
		t.Fatalf("unexpected authenticated destination: %s", data)
	}
}

func TestEgressPolicyRejectsInvalidDestinations(t *testing.T) {
	for name, change := range func() map[string]func(*EgressPolicyDefinition) {
		cases := map[string]func(*EgressPolicyDefinition){
			"no destinations":   func(d *EgressPolicyDefinition) { d.Destinations = nil },
			"invalid name":      func(d *EgressPolicyDefinition) { d.Name = "Not A Name" },
			"host only":         func(d *EgressPolicyDefinition) { d.Destinations[0].Origin = "pypi.org" },
			"non-http scheme":   func(d *EgressPolicyDefinition) { d.Destinations[0].Origin = "ftp://example.com" },
			"path":              func(d *EgressPolicyDefinition) { d.Destinations[0].Origin = "https://example.com/simple" },
			"query":             func(d *EgressPolicyDefinition) { d.Destinations[0].Origin = "https://example.com/?q=1" },
			"fragment":          func(d *EgressPolicyDefinition) { d.Destinations[0].Origin = "https://example.com/#top" },
			"credentials":       func(d *EgressPolicyDefinition) { d.Destinations[0].Origin = "https://user:pw@example.com" },
			"wildcard only":     func(d *EgressPolicyDefinition) { d.Destinations[0].Origin = "https://*" },
			"inner wildcard":    func(d *EgressPolicyDefinition) { d.Destinations[0].Origin = "https://api.*.example.com" },
			"double wildcard":   func(d *EgressPolicyDefinition) { d.Destinations[0].Origin = "https://*.*.example.com" },
			"uppercase host":    func(d *EgressPolicyDefinition) { d.Destinations[0].Origin = "https://EXAMPLE.com" },
			"out of range port": func(d *EgressPolicyDefinition) { d.Destinations[0].Origin = "https://example.com:99999" },
			"cookie header": func(d *EgressPolicyDefinition) {
				d.Destinations[0].Header, d.Destinations[0].SecretName, d.Destinations[0].SecretKey = "Cookie", "token", "value"
			},
			"hop-by-hop header": func(d *EgressPolicyDefinition) {
				d.Destinations[0].Header, d.Destinations[0].SecretName, d.Destinations[0].SecretKey = "Connection", "token", "value"
			},
			"unpaired secret": func(d *EgressPolicyDefinition) {
				d.Destinations[0].Header, d.Destinations[0].SecretName = "Authorization", "token"
			},
			"auth without header": func(d *EgressPolicyDefinition) {
				d.Destinations[0].SecretName, d.Destinations[0].SecretKey = "token", "value"
			},
			"duplicate origin": func(d *EgressPolicyDefinition) {
				d.Destinations = append(d.Destinations, EgressDestination{Origin: "https://example.com"})
			},
		}
		return cases
	}() {
		t.Run(name, func(t *testing.T) {
			d := EgressPolicyDefinition{Name: "egress", Destinations: []EgressDestination{{Origin: "https://example.com"}}}
			change(&d)
			if _, err := RenderEgressPolicyDefinition("kagent", d); err == nil {
				t.Fatal("invalid egress policy accepted")
			}
		})
	}
}

func TestValidOriginsIncludeIPsAndLeftmostWildcards(t *testing.T) {
	for _, origin := range []string{
		"https://pypi.org", "http://10.0.0.1", "https://*.example.com", "https://example.com:8080",
	} {
		if err := validOrigin(origin); err != nil {
			t.Fatalf("valid origin %q rejected: %v", origin, err)
		}
	}
}
