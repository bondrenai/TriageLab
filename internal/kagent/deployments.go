package kagent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os/exec"
	"regexp"
	"slices"
	"strings"
	"time"
)

const deploymentReadLimit = 4 << 20

var (
	namespaceLabel   = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$`)
	conditionReason  = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_,:]{0,127}$`)
	agentChecks      = []string{"Accepted", "ResolvedRefs", "Compatible", "Ready"}
	ErrAgentNotReady = errors.New("The saved agent is not ready. Check Deployment status on the Agents tab.")
)

// DeploymentReader reads the pinned KAgent resources in one explicitly configured namespace.
type DeploymentReader struct {
	kubectl, kubeconfig, kubeContext, namespace string
}

func NewDeploymentReader(kubeconfig, kubeContext, namespace string) (*DeploymentReader, error) {
	if kubeconfig == "" || kubeContext == "" {
		return nil, errors.New("deployment status requires both --status-kubeconfig and --status-context")
	}
	if !namespaceLabel.MatchString(namespace) {
		return nil, errors.New("deployment status requires a valid Kubernetes namespace")
	}
	kubectl, err := exec.LookPath("kubectl")
	if err != nil {
		return nil, errors.New("deployment status requires kubectl on the server")
	}
	return &DeploymentReader{kubectl, kubeconfig, kubeContext, namespace}, nil
}

type Deployment struct {
	Name        string           `json:"name"`
	State       string           `json:"state"`
	Reason      string           `json:"reason,omitempty"`
	Tools       []DiscoveredTool `json:"tools"` // nil means discovery is unavailable, empty means zero tools
	Managed     bool             `json:"managed,omitempty"`
	ManualTools []string         `json:"manual_tools,omitempty"`
}

type DiscoveredTool struct {
	Name           string `json:"name"`
	Description    string `json:"description"`
	SelectionError string `json:"selection_error,omitempty"`
}

type deploymentResource struct {
	Metadata resourceMetadata
	Status   struct {
		ObservedGeneration       int64
		LatestSuccessfulRevision string
		Conditions               []struct {
			Type, Status, Reason string
			ObservedGeneration   int64
		}
		DiscoveredTools []struct{ Name, Description string }
	}
}

// List projects metadata and status only. Spec, Secret data and raw diagnostic
// messages never go to the browser.
func (d *DeploymentReader) List(ctx context.Context, resource string) ([]Deployment, error) {
	var checks []string
	switch resource {
	case "agents":
		checks = agentChecks
	case "modelconfigs":
		checks = []string{"Accepted", "ResolvedRefs"}
	case "remotemcpservers":
		checks = []string{"Accepted"}
	case "sandboxtemplates":
		checks = []string{"Ready"}
	case "egresspolicies":
		// EgressPolicy carries no conditions of its own; grant problems
		// surface on the referencing SandboxTemplate and never block readiness.
	default:
		return nil, errors.New("unsupported deployment resource")
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	path := d.collectionPath(resource)
	query := url.Values{"limit": {"100"}}
	result := []Deployment{}
	remaining := int64(deploymentReadLimit)
	for range 10 {
		data, err := d.read(ctx, path+"?"+query.Encode(), remaining)
		if err != nil {
			return nil, err
		}
		remaining -= int64(len(data))
		var page struct {
			Metadata struct{ Continue string }
			Items    []deploymentResource
		}
		if json.Unmarshal(data, &page) != nil || page.Items == nil {
			return nil, errors.New("invalid KAgent deployment status response")
		}
		for _, item := range page.Items {
			if item.Metadata.Namespace != d.namespace || item.Metadata.Name == "" {
				return nil, errors.New("unexpected KAgent deployment metadata")
			}
			result = append(result, item.deployment(checks, resource))
		}
		if page.Metadata.Continue == "" {
			slices.SortFunc(result, func(a, b Deployment) int { return strings.Compare(a.Name, b.Name) })
			return result, nil
		}
		query.Set("continue", page.Metadata.Continue)
	}
	return nil, errors.New("KAgent deployment status exceeds page limit")
}

// ReadyAgent reads the saved definition and readiness from the same resource snapshot.
func (d *DeploymentReader) ReadyAgent(ctx context.Context, name string) (SavedDefinition, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	data, err := d.resource(ctx, "agents", name)
	if err != nil {
		return SavedDefinition{}, err
	}
	var item deploymentResource
	if err := json.Unmarshal(data, &item); err != nil {
		return SavedDefinition{}, err
	}
	if item.Metadata.Namespace != d.namespace || item.Metadata.Name != name {
		return SavedDefinition{}, ErrResourceNotFound
	}
	saved, err := savedDefinition(data)
	if err != nil {
		return SavedDefinition{}, err
	}
	if item.deployment(agentChecks, "agents").State != "ready" {
		return SavedDefinition{}, ErrAgentNotReady
	}
	saved.PreparedRevision = item.Status.LatestSuccessfulRevision
	return saved, nil
}

// ReadyTarget reads the current generation of a configured deployment without
// requiring an editor-owned definition. Ticket procedures use it for the
// workspace's trusted default agent and its sandbox templates.
func (d *DeploymentReader) ReadyTarget(ctx context.Context, resource, name string) (string, error) {
	checks := []string{"Ready"}
	if resource == "agents" {
		checks = agentChecks
	} else if resource != "sandboxtemplates" {
		return "", ErrResourceInvalid
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	data, err := d.resource(ctx, resource, name)
	if err != nil {
		return "", err
	}
	var item deploymentResource
	if err := json.Unmarshal(data, &item); err != nil {
		return "", err
	}
	if item.Metadata.Namespace != d.namespace || item.Metadata.Name != name || item.deployment(checks, resource).State != "ready" {
		return "", ErrResourceInvalid
	}
	return fmt.Sprintf("%s-%d-%s", item.Metadata.UID, item.Metadata.Generation, item.Status.LatestSuccessfulRevision), nil
}

func (d *DeploymentReader) read(ctx context.Context, path string, limit int64) ([]byte, error) {
	return d.raw(ctx, "get", path, nil, limit)
}

func (d *DeploymentReader) raw(ctx context.Context, verb, path string, body []byte, limit int64) ([]byte, error) {
	args := []string{"--kubeconfig", d.kubeconfig, "--context", d.kubeContext, verb, "--raw", path}
	if body != nil {
		args = append(args, "-f", "-")
	}
	cmd := exec.CommandContext(ctx, d.kubectl, args...)
	if body != nil {
		cmd.Stdin = bytes.NewReader(body)
	}
	var diagnostic boundedDiagnostic
	cmd.Stderr = &diagnostic
	cmd.WaitDelay = time.Second
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	data, err := io.ReadAll(io.LimitReader(stdout, limit+1))
	_ = stdout.Close()
	waitErr := cmd.Wait()
	if int64(len(data)) > limit {
		return nil, errors.New("KAgent deployment status exceeds size limit")
	}
	if err != nil || waitErr != nil {
		for _, known := range []struct {
			reason string
			err    error
		}{
			{"NotFound", ErrResourceNotFound}, {"AlreadyExists", ErrResourceConflict}, {"Conflict", ErrResourceConflict},
			{"Forbidden", ErrResourceForbidden}, {"Invalid", ErrResourceInvalid}, {"BadRequest", ErrResourceInvalid},
		} {
			if bytes.Contains(diagnostic.data, []byte("Error from server ("+known.reason+")")) {
				return nil, known.err
			}
		}
		return nil, fmt.Errorf("Kubernetes %s: %w", verb, errors.Join(err, waitErr))
	}
	return data, nil
}

func (r deploymentResource) deployment(checks []string, resource string) Deployment {
	definition := r.Metadata.Annotations[definitionAnnotation]
	result := Deployment{Name: r.Metadata.Name, State: "pending", Managed: definition != ""}
	mcp := resource == "remotemcpservers"
	if definition != "" {
		switch resource {
		case "remotemcpservers":
			var saved struct {
				ManualTools []string `json:"manual_tools"`
			}
			if json.Unmarshal([]byte(definition), &saved) == nil {
				result.ManualTools = saved.ManualTools
			}
		case "agents":
			// Surface the saved agent's extra MCP tools so runs can show the
			// effective tool set without re-reading the definition.
			var saved struct {
				MCPServers []struct {
					Tools []string `json:"tools"`
				} `json:"mcp_servers"`
			}
			if json.Unmarshal([]byte(definition), &saved) == nil {
				for _, server := range saved.MCPServers {
					result.ManualTools = append(result.ManualTools, server.Tools...)
				}
			}
		}
	}
	if r.Metadata.DeletionTimestamp != nil {
		result.State = "deleting"
		return result
	}
	if checks == nil {
		result.State = "ready"
		return result
	}
	if r.Metadata.Generation == 0 || r.Metadata.Generation != r.Status.ObservedGeneration {
		return result
	}
	ready := 0
	for _, check := range checks {
		for _, condition := range r.Status.Conditions {
			if condition.Type != check || condition.ObservedGeneration != r.Metadata.Generation {
				continue
			}
			if condition.Status == "False" {
				if condition.Reason != "ActorTemplatePending" && condition.Reason != "ActorTemplateRetrying" {
					result.State = "attention"
				}
				if conditionReason.MatchString(condition.Reason) {
					result.Reason = condition.Reason
				}
				return result
			}
			if condition.Status == "True" {
				ready++
				if mcp && condition.Reason == "DiscoverySucceeded" {
					result.Tools = make([]DiscoveredTool, 0, len(r.Status.DiscoveredTools))
					for _, tool := range r.Status.DiscoveredTools {
						result.Tools = append(result.Tools, DiscoveredTool{Name: tool.Name, Description: tool.Description})
					}
				}
				if mcp && condition.Reason == "DiscoveryDisabled" {
					result.Reason = condition.Reason
				}
			}
			break
		}
	}
	if ready == len(checks) {
		result.State = "ready"
	}
	if resource == "sandboxtemplates" && result.Reason == "" {
		// Egress grants resolve independently and never block readiness.
		for _, condition := range r.Status.Conditions {
			if condition.Type == "EgressPoliciesApplied" && condition.Status == "False" && conditionReason.MatchString(condition.Reason) {
				result.Reason = condition.Reason
			}
		}
	}
	return result
}
