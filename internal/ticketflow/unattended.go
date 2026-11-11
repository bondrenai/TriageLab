package ticketflow

import (
	"context"
	"errors"
	"slices"
	"time"

	"github.com/bondrenai/TriageLab/internal/casefile"
)

func (f *Flow) unattended(p Procedure) bool {
	return f.AutoLaunch != nil && f.settings.Policy.AutoRuns && p.Policy.AutoRuns
}

// saveAndMaybeLaunch runs under op with evidence assessed in the current
// tracking, refresh, or routing operation.
func (f *Flow) saveAndMaybeLaunch(ctx context.Context, c Case, t ticket, input casefile.Case) (Case, error) {
	if !c.AutoRun {
		return c, f.save(c)
	}
	limit := f.settings.MaxAutoRunsPerDay
	if limit > 0 {
		cutoff := time.Now().Add(-24 * time.Hour)
		for _, previous := range f.Cases() {
			if previous.Unattended && previous.ApprovedAt.After(cutoff) {
				limit--
			}
		}
		if limit <= 0 {
			c.Reason = "The automatic run limit for the last 24 hours was reached. Approve the run or wait."
			return c, f.save(c)
		}
	}
	if err := f.save(c); err != nil {
		return Case{}, err
	}
	ctx, finish := traced(ctx, "auto_run")
	launched, err := f.launch(ctx, c, c, t, input, f.AutoLaunch, true)
	finish(err)
	if errors.Is(err, ErrBusy) {
		c.Reason = "The workspace is busy. The case is ready to start when the run slot is free."
		return c, f.save(c)
	}
	return launched, err
}

func (f *Flow) poll(ctx context.Context, last int64) int64 {
	if f.oauth != nil && !f.oauth.status().Connected {
		return last
	}
	version := f.Procedure()
	checkCELRoutes := slices.ContainsFunc(append(slices.Clone(version.Additional), version.Procedure), func(p Procedure) bool {
		return f.unattended(p) && p.Policy.MatchCondition != ""
	})
	due := slices.DeleteFunc(f.Cases(), func(c Case) bool {
		if c.Paused || c.RunID != "" {
			return true
		}
		if c.State == "waiting" {
			return false
		}
		p, matched := version.byTag(c.MatchedTag)
		return !checkCELRoutes && (!matched || !f.unattended(p))
	})
	if len(due) == 0 {
		return last
	}
	index := slices.IndexFunc(due, func(c Case) bool { return c.TicketID > last })
	if index < 0 {
		index = 0
	}
	last = due[index].TicketID
	_, _ = f.Refresh(ctx, last)
	return last
}
