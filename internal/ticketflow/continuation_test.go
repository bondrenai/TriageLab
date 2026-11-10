package ticketflow

import (
	"errors"
	"path/filepath"
	"testing"

	"github.com/bondrenai/TriageLab/internal/casefile"
)

func TestContinuationReservesExactInputsAndPreservesOriginalApproval(t *testing.T) {
	f := newFixture(t)
	dir := filepath.Join(t.TempDir(), "cases")
	flow := noteTestFlow(t, dir, f)
	id, saved := completedTicketReport(t, flow, f)
	saved["status"] = "paused"
	before := flow.Cases()[0]
	before.Note = &NoteDelivery{Status: "unconfirmed"}
	if err := flow.save(before); err != nil {
		t.Fatal(err)
	}
	input, err := casefile.Ingest("The customer confirmed the timeout is in milliseconds.", "", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	config := flow.runtime
	config.Agent, config.Template = before.Agent, before.Sandbox
	a := ContinuationApproval{RunID: id, Round: 1, Report: saved, Input: input, Config: config, Actor: "operator"}
	launches := 0
	launch := func(reserve func() error) error {
		if err := reserve(); err != nil {
			return err
		}
		if len(flow.Cases()[0].Continuations) != 1 {
			t.Fatal("work started without a durable continuation approval")
		}
		launches++
		return nil
	}
	if err := flow.ApproveContinuation(a, func(func() error) error { return ErrBusy }); !errors.Is(err, ErrBusy) || len(flow.Cases()[0].Continuations) != 0 {
		t.Fatalf("busy workspace consumed approval: %v", err)
	}
	if err := flow.ApproveContinuation(a, launch); err != nil {
		t.Fatal(err)
	}
	after := flow.Cases()[0]
	if after.RunID != before.RunID || after.Plan != before.Plan || after.Note.Status != "unconfirmed" || f.count() != 0 {
		t.Fatal("continuation changed original approval, note reservation, or wrote to helpdesk")
	}
	if err := flow.Close(); err != nil {
		t.Fatal(err)
	}
	flow = noteTestFlow(t, dir, f)
	a.Input.ID = "a-new-ingestion-id"
	a.Config.TokenEnv = "REFRESHED_TICKET_CREDENTIAL"
	a.Config.ManualAgents = map[string][]string{"new-manual-agent": {"lookup_docs"}}
	if err := flow.ApproveContinuation(a, launch); err != nil {
		t.Fatalf("same approved inputs could not recover after restart: %v", err)
	}
	a.Input.Description = "Different evidence"
	if err := flow.ApproveContinuation(a, launch); !errors.Is(err, ErrChanged) || launches != 2 {
		t.Fatalf("changed inputs reused the durable approval: %v", err)
	}
}

func TestContinuationRejectsUnboundWorkBeforeAdmission(t *testing.T) {
	for _, variant := range []string{"source", "settings", "round", "credential", "attachment", "completed"} {
		t.Run(variant, func(t *testing.T) {
			f := newFixture(t)
			flow := noteTestFlow(t, filepath.Join(t.TempDir(), "cases"), f)
			id, saved := completedTicketReport(t, flow, f)
			saved["status"] = "paused"
			c := flow.Cases()[0]
			config := flow.runtime
			config.Agent, config.Template = c.Agent, c.Sandbox
			input, err := casefile.Ingest("Additional information.", "", nil, nil)
			if err != nil {
				t.Fatal(err)
			}
			a := ContinuationApproval{RunID: id, Round: 1, Report: saved, Input: input, Config: config, Actor: "operator"}
			switch variant {
			case "source":
				saved["case"].(map[string]any)["source"].(map[string]any)["approval_plan"] = "foreign-plan"
			case "settings":
				a.Config.Template = "unapproved-template"
			case "round":
				a.Round = 3
			case "credential":
				a.Input.Description = "test-ticket-credential"
			case "attachment":
				a.Input.Attachments = []casefile.Attachment{{Path: "file.txt", Content: []byte("changed"), SHA256: "old"}}
			case "completed":
				saved["status"] = "completed"
			}
			err = flow.ApproveContinuation(a, func(func() error) error { t.Fatal("unapproved continuation admitted"); return nil })
			if err == nil || len(flow.Cases()[0].Continuations) != 0 || f.count() != 0 {
				t.Fatalf("unbound continuation had effects: %v", err)
			}
		})
	}
}
