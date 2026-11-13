package gitops

import (
	"github.com/bondrenai/TriageLab/internal/ticketflow"
	"testing"
)

func TestProcedureReferencesPreserveOrderAndWorkspace(t *testing.T) {
	c := Connection{Metadata: Metadata{Namespace: "support"}, Spec: ConnectionSpec{Workspace: "lab", ProcedureRefs: []string{"first", "last"}}}
	var first, last Procedure
	first.Metadata = Metadata{Name: "first", Namespace: "support"}
	first.Spec.Workspace = "lab"
	first.Spec.Procedure.Policy = ticketflow.Policy{Tag: "first"}
	last.Metadata = Metadata{Name: "last", Namespace: "support"}
	last.Spec.Workspace = "lab"
	last.Spec.Procedure.Policy = ticketflow.Policy{Tag: "last"}
	v, err := c.Procedures([]Procedure{last, first})
	if err != nil || len(v.Additional) != 1 || v.Additional[0].Policy.Tag != "first" || v.Procedure.Policy.Tag != "last" {
		t.Fatalf("order changed: %+v %v", v, err)
	}
	last.Spec.Workspace = "other"
	if _, err := c.Procedures([]Procedure{first, last}); err == nil {
		t.Fatal("cross-workspace reference accepted")
	}
	c.Spec.ProcedureRefs = []string{"first", "first"}
	if _, err := c.Procedures([]Procedure{first}); err == nil {
		t.Fatal("duplicate reference accepted")
	}
}
