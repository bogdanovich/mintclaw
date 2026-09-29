package tools

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"

	codingremote "github.com/bogdanovich/mintclaw/pkg/coding/remote"
	"github.com/bogdanovich/mintclaw/pkg/runtimecap"
	toolshared "github.com/bogdanovich/mintclaw/pkg/tools/shared"
)

// BrowserCapabilityClient is the coding-side typed view of browser profiles
// admitted by the authenticated capability broker. It deliberately exposes
// neither transport details nor a browser runtime: the gateway remains the
// sole owner of persistent profiles and sessions.
type BrowserCapabilityClient interface {
	runtimecap.BrowserClient
	BrowserCapabilities() []CodingBrowserCapability
	InvokeBrowser(
		context.Context,
		string,
		string,
		map[string]any,
	) *toolshared.ToolResult
	BrowserInvocationStatus(context.Context, string, string) *toolshared.ToolResult
}

type CodingBrowserCapability struct {
	Alias      string
	Revision   string
	Target     string
	Available  bool
	Operations []CodingBrowserOperation
}

type CodingBrowserOperation struct {
	Alias       string
	Risk        codingremote.Risk
	InputSchema json.RawMessage
	ResultKind  string
}

type codingRemoteBrowserCapabilityClient struct {
	remote     *CodingRemoteCapabilityTool
	configured []string
}

var _ BrowserCapabilityClient = (*codingRemoteBrowserCapabilityClient)(nil)

// NewCodingRemoteBrowserCapabilityClient binds the typed browser view to the
// same remote facade that owns discovery revisions and invocation receipts.
// Configured aliases come only from the trusted coding grant, so an unavailable
// broker remains distinguishable from a missing browser configuration.
func NewCodingRemoteBrowserCapabilityClient(
	remote *CodingRemoteCapabilityTool,
	configuredAliases []string,
) (BrowserCapabilityClient, error) {
	if remote == nil || len(configuredAliases) == 0 {
		return nil, errors.New("coding remote browser capability is unavailable")
	}
	configured := append([]string(nil), configuredAliases...)
	for index, alias := range configured {
		configured[index] = strings.TrimSpace(alias)
		if !codingremote.ValidAlias(configured[index]) {
			return nil, errors.New("coding remote browser capability alias is invalid")
		}
	}
	slices.Sort(configured)
	configured = slices.Compact(configured)
	return &codingRemoteBrowserCapabilityClient{remote: remote, configured: configured}, nil
}

func (client *codingRemoteBrowserCapabilityClient) Available() bool {
	for _, capability := range client.BrowserCapabilities() {
		if capability.Available {
			return true
		}
	}
	return false
}

func (client *codingRemoteBrowserCapabilityClient) BrowserCapabilities() []CodingBrowserCapability {
	if client == nil || client.remote == nil {
		return nil
	}
	snapshot := client.remote.currentSnapshot()
	capabilities := make([]CodingBrowserCapability, 0, len(snapshot.Capabilities))
	for _, capability := range snapshot.Capabilities {
		if capability.Kind != codingremote.CapabilityBrowserProfile ||
			!slices.Contains(client.configured, capability.Alias) {
			continue
		}
		operations := make([]CodingBrowserOperation, 0, len(capability.Operations))
		for _, operation := range capability.Operations {
			operations = append(operations, CodingBrowserOperation{
				Alias:       operation.Alias,
				Risk:        operation.Risk,
				InputSchema: append(json.RawMessage(nil), operation.InputSchema...),
				ResultKind:  operation.ResultKind,
			})
		}
		capabilities = append(capabilities, CodingBrowserCapability{
			Alias: capability.Alias, Revision: capability.Revision, Target: capability.Target,
			Available:  capability.Availability == codingremote.AvailabilityAvailable,
			Operations: operations,
		})
	}
	return capabilities
}

func (client *codingRemoteBrowserCapabilityClient) InvokeBrowser(
	ctx context.Context,
	capabilityAlias string,
	operationAlias string,
	input map[string]any,
) *toolshared.ToolResult {
	capability, found := client.browserCapability(capabilityAlias)
	if !found || capability.Availability != codingremote.AvailabilityAvailable {
		return remoteToolError("CAPABILITY_UNAVAILABLE", "browser capability is unavailable; refresh discovery")
	}
	if _, operationFound := snapshotOperation(capability, operationAlias); !operationFound {
		return remoteToolError("OPERATION_UNAVAILABLE", "browser operation is unavailable; refresh discovery")
	}
	if input == nil {
		return remoteToolError("INVALID_ARGUMENTS", "browser input must be an object")
	}
	return client.remote.executeOperation(ctx, "invoke", map[string]any{
		"action": "invoke", "capability": capabilityAlias, "operation": operationAlias, "input": input,
	})
}

func (client *codingRemoteBrowserCapabilityClient) BrowserInvocationStatus(
	ctx context.Context,
	capabilityAlias string,
	invocationID string,
) *toolshared.ToolResult {
	capabilityAlias = strings.TrimSpace(capabilityAlias)
	invocationID = strings.TrimSpace(invocationID)
	if !slices.Contains(client.configured, capabilityAlias) || invocationID == "" {
		return remoteToolError("INVALID_ARGUMENTS", "browser capability and invocation receipt are required")
	}
	return client.remote.executeOperation(ctx, "status", map[string]any{
		"action": "status", "capability": capabilityAlias, "invocation_id": invocationID,
	})
}

func (client *codingRemoteBrowserCapabilityClient) browserCapability(
	alias string,
) (codingremote.CapabilityDescriptor, bool) {
	if client == nil || client.remote == nil {
		return codingremote.CapabilityDescriptor{}, false
	}
	alias = strings.TrimSpace(alias)
	if !slices.Contains(client.configured, alias) {
		return codingremote.CapabilityDescriptor{}, false
	}
	capability, found := snapshotCapability(client.remote.currentSnapshot(), alias)
	return capability, found && capability.Kind == codingremote.CapabilityBrowserProfile
}
