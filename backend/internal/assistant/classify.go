package assistant

import (
	"context"
	"fmt"
	"strings"

	"github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/responses"
	"github.com/openai/openai-go/v3/shared"
)

// Classify : un appel court, sans outils, au modèle rapide — pour une décision
// simple comme « à qui confier cette tâche », pas pour répondre. Sans modèle
// rapide configuré, le modèle principal s'en charge.
func (e *Engine) Classify(ctx context.Context, instructions, input string) (string, error) {
	model, effort := e.fastModel, e.fastEffort
	if model == "" {
		model, effort = e.model, e.effort
	}
	params := responses.ResponseNewParams{
		Model:        model,
		Instructions: openai.String(instructions),
		Input: responses.ResponseNewParamsInputUnion{
			OfInputItemList: []responses.ResponseInputItemUnionParam{
				responses.ResponseInputItemParamOfMessage(input, responses.EasyInputMessageRoleUser),
			},
		},
		Store: openai.Bool(false),
	}
	if effort != "" {
		params.Reasoning = shared.ReasoningParam{Effort: effort}
	}
	resp, err := e.client.Responses.New(ctx, params)
	if err != nil {
		return "", fmt.Errorf("classement : %w", err)
	}
	out := strings.TrimSpace(resp.OutputText())
	if out == "" {
		return "", fmt.Errorf("classement vide")
	}
	return out, nil
}
