package webserver

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"sort"
	"strconv"

	"github.com/bondrenai/TriageLab/internal/agent"
	"github.com/bondrenai/TriageLab/internal/investigation"
	"github.com/bondrenai/TriageLab/internal/kagent"
)

// EnableDeploymentStatus configures the optional reader before the server starts.
func (s *Server) EnableDeploymentStatus(kubeconfig, kubeContext string) error {
	reader, err := kagent.NewDeploymentReader(kubeconfig, kubeContext, s.config.Namespace)
	if err != nil {
		return err
	}
	s.deployments = reader
	return nil
}

func (s *Server) EnableResourceWrites() error {
	if s.deployments == nil {
		return errors.New("resource writes require --status-kubeconfig and --status-context")
	}
	s.resourceWrites = true
	return nil
}

func resourceDefinition[T any](s *Server, w http.ResponseWriter, r *http.Request, resource string, render func(string, T) ([]byte, error), name func(T) string) {
	if !s.resourceWrites {
		fail(w, http.StatusForbidden, "Saving to kagent is disabled for this workspace")
		return
	}
	var saved kagent.SavedDefinition
	var err error
	switch r.Method {
	case http.MethodGet:
		saved, err = s.deployments.Definition(r.Context(), resource, r.URL.Query().Get("name"))
	case http.MethodPost:
		definition, decodeErr := agent.DecodeDefinition[T](http.MaxBytesReader(w, r.Body, agent.MaxDefinitionBytes))
		if decodeErr != nil {
			fail(w, http.StatusBadRequest, decodeErr.Error())
			return
		}
		if resource == "agents" && name(definition) == s.config.Agent {
			fail(w, http.StatusBadRequest, "The workspace's default investigator is managed separately")
			return
		}
		if resource == "sandboxtemplates" && name(definition) == s.config.Template {
			fail(w, http.StatusBadRequest, "The workspace's default sandbox template is managed separately")
			return
		}
		manifest, renderErr := render(s.config.Namespace, definition)
		if renderErr != nil {
			fail(w, http.StatusBadRequest, renderErr.Error())
			return
		}
		var revision string
		if match := r.Header.Get("If-Match"); match != "" {
			revision, err = strconv.Unquote(match)
			if err != nil || revision == "" || len(revision) > 256 {
				fail(w, http.StatusBadRequest, "Invalid definition revision")
				return
			}
		}
		encoded, marshalErr := json.Marshal(definition)
		if marshalErr != nil || len(encoded) > agent.MaxDefinitionBytes {
			fail(w, http.StatusBadRequest, "The saved definition exceeds 128 KiB")
			return
		}
		saved, err = s.deployments.Save(r.Context(), resource, manifest, encoded, revision)
	default:
		fail(w, http.StatusMethodNotAllowed, "Method not allowed")
		return
	}
	switch {
	case err == nil:
		jsonResponse(w, http.StatusOK, saved)
	case errors.Is(err, kagent.ErrResourceNotFound):
		fail(w, http.StatusNotFound, err.Error())
	case errors.Is(err, kagent.ErrResourceConflict):
		fail(w, http.StatusConflict, err.Error())
	case errors.Is(err, kagent.ErrResourceForbidden):
		fail(w, http.StatusForbidden, err.Error())
	case errors.Is(err, kagent.ErrResourceInvalid):
		fail(w, http.StatusUnprocessableEntity, err.Error())
	default:
		fail(w, http.StatusBadGateway, "Could not verify the Kubernetes operation. Refresh deployment status before trying again.")
	}
}

func (s *Server) deploymentStatus(w http.ResponseWriter, r *http.Request, resource string) {
	if r.Method != http.MethodGet {
		fail(w, http.StatusMethodNotAllowed, "Method not allowed")
		return
	}
	response := struct {
		Enabled   bool                `json:"enabled"`
		Resources []kagent.Deployment `json:"resources"`
	}{Resources: []kagent.Deployment{}}
	if s.deployments != nil {
		resources, err := s.deployments.List(r.Context(), resource)
		if err != nil {
			fail(w, http.StatusBadGateway, "Could not read deployment status. Check the configured cluster connection and namespace permissions.")
			return
		}
		for _, resource := range resources {
			for i := range resource.Tools {
				tool := &resource.Tools[i]
				if err := agent.ValidateExtraTools([]string{tool.Name}); err != nil {
					tool.SelectionError = err.Error()
				}
			}
		}
		response.Enabled = true
		response.Resources = resources
	}
	jsonResponse(w, http.StatusOK, response)
}

func renderManifest[T any](w http.ResponseWriter, r *http.Request, namespace string, render func(string, T) ([]byte, error)) {
	if r.Method != http.MethodPost {
		fail(w, http.StatusMethodNotAllowed, "Method not allowed")
		return
	}
	definition, err := agent.DecodeDefinition[T](http.MaxBytesReader(w, r.Body, agent.MaxDefinitionBytes))
	if err != nil {
		fail(w, http.StatusBadRequest, err.Error())
		return
	}
	manifest, err := render(namespace, definition)
	if err != nil {
		fail(w, http.StatusBadRequest, err.Error())
		return
	}
	jsonResponse(w, http.StatusOK, json.RawMessage(manifest))
}

func (s *Server) manualAgentNames() []string {
	names := []string{}
	for name := range s.config.ManualAgents {
		if name != s.config.Agent {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	return names
}

func (s *Server) selectInvestigator(ctx context.Context, name string) (investigation.Config, []string, error) {
	config, tools, err := s.config.SelectManualAgent(name)
	if err == nil || !s.resourceWrites || !agent.ValidResourceName(name) {
		return config, tools, err
	}
	saved, err := s.deployments.ReadyAgent(ctx, name)
	if err != nil {
		return config, nil, err
	}
	tools, err = savedAgentTools(saved)
	if err != nil {
		return config, nil, err
	}
	config.Agent = name
	return config, tools, nil
}

var errSandboxForbidden = errors.New("choosing a different sandbox template requires a workspace with resource writes")

// selectSandbox resolves a run's sandbox template. The configured default is
// always allowed; any other template needs resource writes and a ready,
// revision-pinned target, matching ticket procedure routing.
func (s *Server) selectSandbox(ctx context.Context, name string) (string, error) {
	if name == "" || name == s.config.Template {
		return s.config.Template, nil
	}
	if !s.resourceWrites || !agent.ValidResourceName(name) {
		return "", errSandboxForbidden
	}
	if _, err := s.deployments.ReadyTarget(ctx, "sandboxtemplates", name); err != nil {
		return "", err
	}
	return name, nil
}

func savedAgentTools(saved kagent.SavedDefinition) ([]string, error) {
	definition, err := agent.DecodeDefinition[agent.Definition](bytes.NewReader(saved.Definition))
	if err != nil {
		return nil, err
	}
	var tools []string
	for _, server := range definition.MCPServers {
		tools = append(tools, server.Tools...)
	}
	if err := agent.ValidateExtraTools(tools); err != nil {
		return nil, err
	}
	return tools, nil
}
