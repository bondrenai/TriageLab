// Package gitops reads TriageLab's declarative ticket configuration. It owns
// no sandbox or investigation lifecycle and never writes resource specs.
package gitops

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"regexp"

	"github.com/bondrenai/TriageLab/internal/ticketflow"
)

const APIVersion = "triagelab.bondren.ai/v1alpha1"

var namePattern = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$`)

type Metadata struct {
	Name              string  `json:"name"`
	Namespace         string  `json:"namespace"`
	UID               string  `json:"uid"`
	ResourceVersion   string  `json:"resourceVersion"`
	Generation        int64   `json:"generation"`
	DeletionTimestamp *string `json:"deletionTimestamp,omitempty"`
}

type SecretRef struct {
	Name string `json:"name"`
	Key  string `json:"key"`
}

type ConnectionSpec struct {
	Workspace      string            `json:"workspace"`
	Label          string            `json:"label,omitempty"`
	Config         ticketflow.Config `json:"config"`
	TokenSecretRef *SecretRef        `json:"tokenSecretRef,omitempty"`
	// ProcedureRefs are evaluated in order; every procedure must match its
	// admission rules, including the last one.
	ProcedureRefs []string `json:"procedureRefs"`
}

type Connection struct {
	APIVersion string         `json:"apiVersion"`
	Kind       string         `json:"kind"`
	Metadata   Metadata       `json:"metadata"`
	Spec       ConnectionSpec `json:"spec"`
	Status     Status         `json:"status,omitempty"`
}

type Procedure struct {
	APIVersion string   `json:"apiVersion"`
	Kind       string   `json:"kind"`
	Metadata   Metadata `json:"metadata"`
	Spec       struct {
		Workspace string               `json:"workspace"`
		Procedure ticketflow.Procedure `json:"procedure"`
	} `json:"spec"`
}

type Condition struct {
	Type               string `json:"type"`
	Status             string `json:"status"`
	Reason             string `json:"reason"`
	Message            string `json:"message"`
	ObservedGeneration int64  `json:"observedGeneration"`
	LastTransitionTime string `json:"lastTransitionTime"`
}

type Status struct {
	ObservedGeneration int64       `json:"observedGeneration"`
	ConnectionID       string      `json:"connectionID,omitempty"`
	Conditions         []Condition `json:"conditions"`
}

type Snapshot struct {
	Connections []Connection
	Procedures  []Procedure
}

// StateKey is stable across deletion/recreation and process restarts. The
// full digest names private state; callers must reject short UI ID collisions.
func StateKey(server, namespace, workspace, name string) string {
	data, _ := json.Marshal([]string{server, namespace, workspace, name})
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func (c Connection) Procedures(all []Procedure) (ticketflow.ProcedureVersion, error) {
	var result ticketflow.ProcedureVersion
	if len(c.Spec.ProcedureRefs) < 1 || len(c.Spec.ProcedureRefs) > 8 {
		return result, errors.New("one to eight procedure references are required")
	}
	seen := map[string]bool{}
	ordered := make([]ticketflow.Procedure, 0, len(c.Spec.ProcedureRefs))
	for _, name := range c.Spec.ProcedureRefs {
		if !namePattern.MatchString(name) || seen[name] {
			return result, errors.New("invalid or duplicate procedure reference")
		}
		seen[name] = true
		found := false
		for _, p := range all {
			if p.Metadata.Name == name && p.Metadata.Namespace == c.Metadata.Namespace &&
				p.Spec.Workspace == c.Spec.Workspace && p.Metadata.DeletionTimestamp == nil {
				ordered = append(ordered, p.Spec.Procedure)
				found = true
				break
			}
		}
		if !found {
			return result, errors.New("a referenced procedure is missing from this workspace")
		}
	}
	result.Procedure = ordered[len(ordered)-1]
	result.Additional = ordered[:len(ordered)-1]
	return result, nil
}
