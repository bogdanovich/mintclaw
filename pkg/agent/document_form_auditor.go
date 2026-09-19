package agent

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"strings"

	"github.com/bogdanovich/mintclaw/pkg/config"
	"github.com/bogdanovich/mintclaw/pkg/document"
	"github.com/bogdanovich/mintclaw/pkg/providers"
)

const maxDocumentFormAuditRequestBytes = 256 * 1024

type documentFormAuditModel struct {
	provider providers.LLMProvider
	model    string
}

type documentFormAuditor struct {
	models     map[string]documentFormAuditModel
	identities map[string]string
}

func newDocumentFormAuditor(cfg *config.Config, agent *AgentInstance) *documentFormAuditor {
	if cfg == nil || agent == nil || strings.TrimSpace(cfg.Tools.Document.AuditModel) == "" {
		return nil
	}
	aliases := append(
		[]string{cfg.Tools.Document.AuditModel},
		cfg.Tools.Document.AuditEquivalentFallbacks...,
	)
	auditor := &documentFormAuditor{
		models:     make(map[string]documentFormAuditModel, len(aliases)),
		identities: make(map[string]string, len(aliases)),
	}
	for _, alias := range aliases {
		selection, err := resolveModelSelection(cfg, alias, agent.Workspace)
		if err != nil {
			continue
		}
		candidate, ok := candidateFromModelSelection(selection)
		if !ok {
			continue
		}
		auditor.identities[alias] = documentFormAuditModelIdentity(
			alias,
			candidate.Provider,
			candidate.Model,
		)
		provider := documentAuditExistingProvider(agent, candidate)
		model := candidate.Model
		if provider == nil {
			created, resolvedModel, createErr := providers.CreateProviderFromConfig(selection.modelConfig)
			if createErr != nil || created == nil || strings.TrimSpace(resolvedModel) == "" {
				continue
			}
			provider = created
			model = resolvedModel
			auditor.identities[alias] = documentFormAuditModelIdentity(alias, candidate.Provider, model)
			if stateful, statefulProvider := created.(providers.StatefulProvider); statefulProvider {
				agent.ownedProviders = append(agent.ownedProviders, stateful)
			}
		}
		auditor.models[alias] = documentFormAuditModel{provider: provider, model: model}
	}
	if len(auditor.models) == 0 {
		return nil
	}
	return auditor
}

func documentFormAuditModelIdentity(alias, provider, model string) string {
	digest := sha256.Sum256([]byte(strings.Join([]string{
		"mintclaw.document-form-audit-model.v1",
		strings.TrimSpace(alias),
		strings.TrimSpace(provider),
		strings.TrimSpace(model),
	}, "\x00")))
	return "audit_model_" + hex.EncodeToString(digest[:])
}

func documentAuditExistingProvider(
	agent *AgentInstance,
	candidate providers.FallbackCandidate,
) providers.LLMProvider {
	if agent == nil {
		return nil
	}
	if provider := agent.CandidateProviders[candidateProviderKey(candidate)]; provider != nil {
		return provider
	}
	for index, existing := range agent.Candidates {
		if existing.StableKey() != candidate.StableKey() || existing.Provider != candidate.Provider ||
			existing.Model != candidate.Model {
			continue
		}
		if provider := agent.CandidateProviders[candidateProviderKey(existing)]; provider != nil {
			return provider
		}
		if index == 0 {
			return agent.Provider
		}
	}
	for _, existing := range agent.LightCandidates {
		if existing.StableKey() == candidate.StableKey() && existing.Provider == candidate.Provider &&
			existing.Model == candidate.Model {
			return agent.LightProvider
		}
	}
	return nil
}

func (auditor *documentFormAuditor) AuditForm(
	ctx context.Context,
	alias string,
	view document.FormAuditView,
) (document.FormAuditProposal, error) {
	if auditor == nil {
		return document.FormAuditProposal{}, document.ErrFormAuditUnavailable
	}
	resolved, ok := auditor.models[alias]
	if !ok || resolved.provider == nil || strings.TrimSpace(resolved.model) == "" {
		return document.FormAuditProposal{}, document.ErrFormAuditUnavailable
	}
	payload, err := encodeDocumentFormAuditView(view)
	if err != nil {
		return document.FormAuditProposal{}, document.ErrFormAuditUnavailable
	}
	defer clear(payload)
	response, err := resolved.provider.Chat(
		ctx,
		[]providers.Message{
			{Role: "system", Content: documentFormAuditSystemPrompt},
			{Role: "user", Content: string(payload)},
		},
		nil,
		resolved.model,
		map[string]any{"temperature": 0},
	)
	if err != nil || response == nil || len(response.ToolCalls) != 0 {
		return document.FormAuditProposal{}, document.ErrFormAuditUnavailable
	}
	proposal, err := decodeDocumentFormAuditProposal(response.Content)
	if err != nil {
		return document.FormAuditProposal{}, document.ErrFormAuditUnavailable
	}
	return proposal, nil
}

const documentFormAuditSystemPrompt = `You audit one protected PDF form assignment set. ` +
	`Return exactly one JSON object and no markdown. Allowed shapes: ` +
	`{"decision":"pass"} or {"decision":"block","findings":[{"field_id":"exact supplied id",` +
	`"code":"field_ambiguous|field_conflicting|field_confirmation_required|field_invalid"}]}. ` +
	`Use only field IDs present in the request. Never copy, transform, summarize, or emit a field value. ` +
	`Pass only when the supplied assignments are internally coherent with the bounded field schema. ` +
	`Your output is advisory and cannot confirm or modify an assignment.`

type documentFormAuditPayload struct {
	SchemaVersion  string                          `json:"schema_version"`
	PolicyRevision string                          `json:"policy_revision"`
	SchemaDigest   string                          `json:"schema_digest"`
	Fields         []documentFormAuditFieldPayload `json:"fields"`
}

type documentFormAuditFieldPayload struct {
	FieldID     string                       `json:"field_id"`
	Name        string                       `json:"name,omitempty"`
	Alternate   string                       `json:"alternate_name,omitempty"`
	Kind        document.FormFieldKind       `json:"kind"`
	Required    bool                         `json:"required"`
	MultiSelect bool                         `json:"multi_select,omitempty"`
	Editable    bool                         `json:"editable,omitempty"`
	DateFormat  string                       `json:"date_format,omitempty"`
	Options     []document.FormFieldOption   `json:"options,omitempty"`
	Existing    bool                         `json:"existing,omitempty"`
	State       document.FormValueState      `json:"state,omitempty"`
	Source      document.FormValueSource     `json:"source,omitempty"`
	Confidence  document.FormValueConfidence `json:"confidence,omitempty"`
	Validation  document.FormValueValidation `json:"validation,omitempty"`
	Value       document.FormProtectedValue  `json:"value,omitzero"`
}

func encodeDocumentFormAuditView(view document.FormAuditView) ([]byte, error) {
	payload := documentFormAuditPayload{
		SchemaVersion:  document.FormAuditPromptRevision,
		PolicyRevision: view.PolicyRevision,
		SchemaDigest:   view.SchemaDigest,
		Fields:         make([]documentFormAuditFieldPayload, 0, len(view.Fields)),
	}
	for _, field := range view.Fields {
		payload.Fields = append(payload.Fields, documentFormAuditFieldPayload{
			FieldID: field.Schema.ID, Name: field.Schema.Name, Alternate: field.Schema.AlternateName,
			Kind: field.Schema.Kind, Required: field.Schema.Required,
			MultiSelect: field.Schema.MultiSelect, Editable: field.Schema.Editable,
			DateFormat: field.Schema.DateFormat,
			Options:    append([]document.FormFieldOption(nil), field.Schema.Options...),
			Existing:   field.Existing, State: field.State.State, Source: field.State.Source,
			Confidence: field.State.Confidence, Validation: field.State.Validation,
			Value: field.Value,
		})
	}
	encoded, err := json.Marshal(payload)
	if err != nil || len(encoded) == 0 || len(encoded) > maxDocumentFormAuditRequestBytes {
		clear(encoded)
		return nil, errors.New("document form audit request exceeds its protected bound")
	}
	return encoded, nil
}

func decodeDocumentFormAuditProposal(content string) (document.FormAuditProposal, error) {
	if strings.TrimSpace(content) != content || content == "" || len(content) > 64*1024 {
		return document.FormAuditProposal{}, errors.New("document form audit response is invalid")
	}
	decoder := json.NewDecoder(bytes.NewBufferString(content))
	decoder.DisallowUnknownFields()
	var proposal document.FormAuditProposal
	if err := decoder.Decode(&proposal); err != nil {
		return document.FormAuditProposal{}, errors.New("document form audit response is invalid")
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return document.FormAuditProposal{}, errors.New("document form audit response has trailing data")
	}
	return proposal, nil
}

func documentFormAuditPolicy(
	cfg *config.Config,
	auditor *documentFormAuditor,
) document.FormAuditPolicy {
	if cfg == nil || auditor == nil {
		return document.FormAuditPolicy{}
	}
	identities := make([]string, len(cfg.Tools.Document.AuditEquivalentFallbacks))
	for index, alias := range cfg.Tools.Document.AuditEquivalentFallbacks {
		identities[index] = auditor.identities[alias]
	}
	return document.FormAuditPolicy{
		PrimaryModel:                 cfg.Tools.Document.AuditModel,
		PrimaryIdentity:              auditor.identities[cfg.Tools.Document.AuditModel],
		EquivalentFallbacks:          append([]string(nil), cfg.Tools.Document.AuditEquivalentFallbacks...),
		EquivalentFallbackIdentities: identities,
		PromptRevision:               document.FormAuditPromptRevision,
	}
}
