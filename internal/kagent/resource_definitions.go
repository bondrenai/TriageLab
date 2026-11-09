package kagent

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"time"
)

const (
	resourceAPIVersion   = "api.kagent.dev/v1alpha3"
	definitionAnnotation = "triagelab.bondren.ai/definition"
	runtimeAnnotation    = "triagelab.bondren.ai/runtime-for"
)

var (
	ErrResourceNotFound  = errors.New("definition not found")
	ErrResourceConflict  = errors.New("the name already exists or the definition changed since you opened it")
	ErrResourceForbidden = errors.New("the cluster did not grant access to this resource")
	ErrResourceInvalid   = errors.New("Kubernetes rejected the resource definition")
	resourceName         = regexp.MustCompile(`^[a-z0-9]([a-z0-9.-]{0,251}[a-z0-9])?$`)
)

type resourceMetadata struct {
	Name              string            `json:"name"`
	Namespace         string            `json:"namespace"`
	UID               string            `json:"uid,omitempty"`
	Generation        int64             `json:"generation,omitempty"`
	ResourceVersion   string            `json:"resourceVersion,omitempty"`
	DeletionTimestamp *string           `json:"deletionTimestamp,omitempty"`
	Annotations       map[string]string `json:"annotations,omitempty"`
}

type resourceObject struct {
	APIVersion string           `json:"apiVersion"`
	Kind       string           `json:"kind"`
	Metadata   resourceMetadata `json:"metadata"`
	Spec       json.RawMessage  `json:"spec"`
	Items      []resourceObject `json:"items,omitempty"`
}

type SavedDefinition struct {
	Definition       json.RawMessage `json:"definition"`
	Revision         string          `json:"revision"`
	PreparedRevision string          `json:"-"`
}

func (d *DeploymentReader) collectionPath(resource string) string {
	return "/apis/" + resourceAPIVersion + "/namespaces/" + d.namespace + "/" + resource
}

func (d *DeploymentReader) Definition(ctx context.Context, resource, name string) (SavedDefinition, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	data, err := d.resource(ctx, resource, name)
	if err != nil {
		return SavedDefinition{}, err
	}
	return savedDefinition(data)
}

func (d *DeploymentReader) resource(ctx context.Context, resource, name string) ([]byte, error) {
	if !resourceName.MatchString(name) {
		return nil, ErrResourceInvalid
	}
	switch resource {
	case "agents", "modelconfigs", "remotemcpservers", "harnesses", "sandboxtemplates", "egresspolicies":
		return d.read(ctx, d.collectionPath(resource)+"/"+name, deploymentReadLimit)
	default:
		return nil, ErrResourceInvalid
	}
}

func savedDefinition(data []byte) (SavedDefinition, error) {
	var item resourceObject
	if err := json.Unmarshal(data, &item); err != nil {
		return SavedDefinition{}, err
	}
	definition := json.RawMessage(item.Metadata.Annotations[definitionAnnotation])
	var source struct{ Name string }
	if json.Unmarshal(definition, &source) != nil || source.Name == "" || source.Name != item.Metadata.Name || item.Metadata.DeletionTimestamp != nil {
		return SavedDefinition{}, ErrResourceNotFound
	}
	// Status updates change resourceVersion without changing the editable spec.
	hash := sha256.Sum256(definition)
	return SavedDefinition{Definition: definition, Revision: fmt.Sprintf("%s-%d-%x", item.Metadata.UID, item.Metadata.Generation, hash)}, nil
}

// Save accepts only the typed editor's rendered manifest. Create is atomic with
// respect to name collisions. Update retains metadata and uses resourceVersion
// to detect concurrent writes between the read and the PUT.
func (d *DeploymentReader) Save(ctx context.Context, resource string, manifest, definition []byte, revision string) (SavedDefinition, error) {
	ctx, cancel := context.WithTimeout(ctx, 12*time.Second)
	defer cancel()
	var item resourceObject
	if err := json.Unmarshal(manifest, &item); err != nil {
		return SavedDefinition{}, err
	}
	var runtime *resourceObject
	if item.Kind == "List" && resource == "agents" {
		switch len(item.Items) {
		case 1:
			item = item.Items[0]
		case 2:
			runtime = &item.Items[0]
			item = item.Items[1]
		default:
			return SavedDefinition{}, ErrResourceInvalid
		}
	}
	kind := map[string]string{
		"agents": "Agent", "modelconfigs": "ModelConfig", "remotemcpservers": "RemoteMCPServer",
		"sandboxtemplates": "SandboxTemplate", "egresspolicies": "EgressPolicy",
	}[resource]
	if kind == "" || !d.validObject(item, kind) || (runtime != nil && !d.validObject(*runtime, "Harness")) {
		return SavedDefinition{}, ErrResourceInvalid
	}
	path := d.collectionPath(resource)
	verb := "create"
	var current []byte
	if revision != "" || runtime != nil {
		data, err := d.resource(ctx, resource, item.Metadata.Name)
		if revision == "" {
			if err == nil {
				return SavedDefinition{}, ErrResourceConflict
			}
			if !errors.Is(err, ErrResourceNotFound) {
				return SavedDefinition{}, err
			}
		} else {
			if err != nil {
				return SavedDefinition{}, err
			}
			saved, err := savedDefinition(data)
			if err != nil || saved.Revision != revision {
				return SavedDefinition{}, ErrResourceConflict
			}
			current = data
			verb = "replace"
			path += "/" + item.Metadata.Name
		}
	}
	var payload []byte
	var err error
	if current == nil {
		item.Metadata.Annotations = map[string]string{definitionAnnotation: string(definition)}
		payload, err = json.Marshal(item)
	} else {
		payload, err = updateDefinition(current, item.Spec, definition)
	}
	if err != nil {
		return SavedDefinition{}, err
	}
	if runtime != nil {
		if err := d.stageRuntime(ctx, *runtime, item.Metadata.Name); err != nil {
			return SavedDefinition{}, err
		}
	}
	data, err := d.raw(ctx, verb, path, payload, deploymentReadLimit)
	if err != nil {
		return SavedDefinition{}, err
	}
	return savedDefinition(data)
}

func (d *DeploymentReader) validObject(item resourceObject, kind string) bool {
	return item.APIVersion == resourceAPIVersion && item.Kind == kind &&
		item.Metadata.Namespace == d.namespace && resourceName.MatchString(item.Metadata.Name) && len(item.Spec) != 0
}

func updateDefinition(current, spec, definition []byte) ([]byte, error) {
	var item, metadata map[string]json.RawMessage
	var annotations map[string]string
	if err := json.Unmarshal(current, &item); err != nil {
		return nil, err
	}
	if err := json.Unmarshal(item["metadata"], &metadata); err != nil {
		return nil, err
	}
	if err := json.Unmarshal(metadata["annotations"], &annotations); err != nil {
		return nil, err
	}
	annotations[definitionAnnotation] = string(definition)
	metadata["annotations"], _ = json.Marshal(annotations)
	item["metadata"], _ = json.Marshal(metadata)
	item["spec"] = spec
	return json.Marshal(item)
}

func (d *DeploymentReader) stageRuntime(ctx context.Context, runtime resourceObject, agent string) error {
	runtime.Metadata.Annotations = map[string]string{runtimeAnnotation: agent}
	payload, err := json.Marshal(runtime)
	if err != nil {
		return err
	}
	if _, err := d.raw(ctx, "create", d.collectionPath("harnesses"), payload, deploymentReadLimit); !errors.Is(err, ErrResourceConflict) {
		return err
	}
	data, err := d.resource(ctx, "harnesses", runtime.Metadata.Name)
	if err != nil {
		return err
	}
	var existing resourceObject
	if json.Unmarshal(data, &existing) != nil || !d.validObject(existing, "Harness") ||
		existing.Metadata.DeletionTimestamp != nil || existing.Metadata.Annotations[runtimeAnnotation] != agent {
		return ErrResourceConflict
	}
	return nil
}

type boundedDiagnostic struct{ data []byte }

func (b *boundedDiagnostic) Write(p []byte) (int, error) {
	b.data = append(b.data, p[:min(len(p), 8192-len(b.data))]...)
	return len(p), nil
}
