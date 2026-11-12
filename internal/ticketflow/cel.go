package ticketflow

import (
	"context"
	"errors"
	"strconv"
	"strings"

	"cel.dev/cel-go/cel"
	"cel.dev/cel-go/common/types"
)

const maxAutoRunConditionBytes = 4096

type conditionSyntaxError struct {
	issues []*cel.Error
}

func (e *conditionSyntaxError) Error() string {
	return "ticket condition must be valid CEL using ticket attributes"
}

type ConditionDiagnostic struct {
	Message string `json:"message"`
	Line    int    `json:"line,omitempty"`
	Column  int    `json:"column,omitempty"`
}

// ValidateTicketCondition checks an editor draft without reading a ticket or
// changing the saved procedure. CEL locations use one-based lines and columns.
func (f *Flow) ValidateTicketCondition(expression string) []ConditionDiagnostic {
	if f.hasSecret([]byte(expression)) {
		return []ConditionDiagnostic{{Message: "Remove the configured credential from the condition."}}
	}
	_, err := compileTicketCondition(expression)
	if err == nil {
		return []ConditionDiagnostic{}
	}
	var syntax *conditionSyntaxError
	if errors.As(err, &syntax) {
		diagnostics := make([]ConditionDiagnostic, 0, min(len(syntax.issues), 8))
		for _, issue := range syntax.issues[:cap(diagnostics)] {
			diagnostics = append(diagnostics, ConditionDiagnostic{
				Message: bounded(issue.Message, 240), Line: issue.Location.Line(), Column: issue.Location.Column() + 1,
			})
		}
		return diagnostics
	}
	return []ConditionDiagnostic{{Message: err.Error()}}
}

func compileTicketCondition(expression string) (cel.Program, error) {
	if expression == "" {
		return nil, nil
	}
	if len(expression) > maxAutoRunConditionBytes || strings.TrimSpace(expression) == "" {
		return nil, errors.New("ticket condition must contain a CEL expression of at most 4096 bytes")
	}
	env, err := cel.NewEnv(cel.Variable("ticket", cel.MapType(cel.StringType, cel.DynType)),
		cel.ParserExpressionSizeLimit(maxAutoRunConditionBytes), cel.ParserRecursionLimit(64))
	if err != nil {
		return nil, err
	}
	ast, issues := env.Compile(expression)
	if diagnostics := issues.Errors(); len(diagnostics) > 0 {
		return nil, &conditionSyntaxError{issues: diagnostics}
	}
	if !ast.OutputType().IsExactType(cel.BoolType) {
		return nil, errors.New("ticket condition must return a boolean")
	}
	return env.Program(ast, cel.CostLimit(10000), cel.InterruptCheckFrequency(100))
}

func evaluateTicketCondition(ctx context.Context, expression, provider string, t ticket) (bool, error) {
	program, err := compileTicketCondition(expression)
	if err != nil {
		return false, err
	}
	if program == nil {
		return true, nil
	}
	fields := make(map[string]any, len(t.Fields))
	for _, field := range t.Fields {
		fields[strconv.FormatInt(field.ID, 10)] = field.Value
	}
	tags := t.Tags
	if tags == nil {
		tags = []string{}
	}
	attributes := map[string]any{
		"source": provider, "id": t.ID, "requester_id": t.Requester,
		"subject": t.Subject, "status": t.Status, "priority": t.Priority,
		"tags": tags, "custom_fields": fields, "updated_at": t.Updated,
	}
	result, _, err := program.ContextEval(ctx, map[string]any{"ticket": attributes})
	if err != nil {
		return false, errors.New("ticket condition could not be evaluated")
	}
	return result == types.True, nil
}
