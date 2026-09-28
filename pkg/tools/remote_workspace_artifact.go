package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/bogdanovich/mintclaw/pkg/config"
	"github.com/bogdanovich/mintclaw/pkg/nodes"
	toolshared "github.com/bogdanovich/mintclaw/pkg/tools/shared"
)

// RemoteWorkspaceArtifact is the model-safe metadata for one immutable job
// artifact. Hidden job and node identities remain inside the router.
type RemoteWorkspaceArtifact struct {
	Target      string
	Ref         string
	Name        string
	State       string
	Size        int64
	SHA256      string
	ContentType string
}

// RemoteWorkspaceArtifactRouter composes the existing owner-bound job and
// transfer adapters without exposing either generic node RPC or remote paths.
type RemoteWorkspaceArtifactRouter struct {
	cfg    *config.Config
	source NodeFileTransferSource
	jobs   *RemoteWorkspaceNodeRouter
}

func NewRemoteWorkspaceArtifactRouter(
	cfg *config.Config,
	source NodeFileTransferSource,
	agentID string,
) (*RemoteWorkspaceArtifactRouter, error) {
	jobs, err := NewRemoteWorkspaceNodeRouter(cfg, source, agentID, "job_artifacts")
	if err != nil {
		return nil, err
	}
	return &RemoteWorkspaceArtifactRouter{cfg: cfg, source: source, jobs: jobs}, nil
}

func (router *RemoteWorkspaceArtifactRouter) Describe(
	ctx context.Context,
	startCtx context.Context,
	workspaceAlias string,
	jobInvocationID string,
	artifactRef string,
) (RemoteWorkspaceArtifact, error) {
	artifact, _, _, err := router.resolve(ctx, startCtx, workspaceAlias, jobInvocationID, artifactRef)
	return artifact, err
}

func (router *RemoteWorkspaceArtifactRouter) FetchRange(
	ctx context.Context,
	startCtx context.Context,
	workspaceAlias string,
	jobInvocationID string,
	artifactRef string,
	offset int64,
	limit int,
) (RemoteWorkspaceArtifact, NodeDownloadedArtifactChunk, error) {
	binding, ok := router.jobs.byAlias[workspaceAlias]
	if !ok || !binding.allowJobs {
		return RemoteWorkspaceArtifact{}, NodeDownloadedArtifactChunk{}, ErrRemoteWorkspaceUnavailable
	}
	transferCallID := stableNodeInvocationID(
		"coding_artifact",
		jobInvocationID,
		artifactRef,
	)
	transferCtx := toolshared.WithToolCallID(ctx, transferCallID)
	principal, executionCallID, err := nodeInvocationIdentity(transferCtx)
	if err != nil {
		return RemoteWorkspaceArtifact{}, NodeDownloadedArtifactChunk{}, err
	}
	storedToolCallID := stableNodeInvocationID("file_call", executionCallID)
	owner, err := nodeFileArtifactOwner(transferCtx, principal, storedToolCallID)
	if err != nil {
		return RemoteWorkspaceArtifact{}, NodeDownloadedArtifactChunk{}, err
	}
	retained, retainedFound, err := router.source.LookupInvocationByToolCall(principal, storedToolCallID)
	if err != nil {
		return RemoteWorkspaceArtifact{}, NodeDownloadedArtifactChunk{}, err
	}
	var artifact RemoteWorkspaceArtifact
	var jobID string
	var revision string
	if retainedFound {
		var input nodeFileTransferPlanInput
		if json.Unmarshal(retained.Plan.Input, &input) != nil ||
			input.SourceKind != nodes.JobArtifactTransferSourceKind || input.ArtifactRef != artifactRef ||
			input.JobID == "" || input.DiscoveryRevision == "" || input.Deliver == nil || *input.Deliver {
			return RemoteWorkspaceArtifact{}, NodeDownloadedArtifactChunk{}, ErrRemoteWorkspaceUnavailable
		}
		jobID = input.JobID
		revision = input.DiscoveryRevision
	} else {
		var job remoteWorkspaceJobAuthority
		artifact, job, revision, err = router.resolve(
			ctx,
			startCtx,
			workspaceAlias,
			jobInvocationID,
			artifactRef,
		)
		if err != nil {
			return RemoteWorkspaceArtifact{}, NodeDownloadedArtifactChunk{}, err
		}
		jobID = job.id
	}
	tool := NewNodeDownloadTool(NewNodeToolOptions(router.cfg), router.source)
	result := tool.Execute(transferCtx, map[string]any{
		"target": binding.config.Target, "job_id": jobID, "artifact_ref": artifactRef,
		"deliver": false, "discovery_revision": revision,
	})
	if result == nil || result.IsError {
		return RemoteWorkspaceArtifact{}, NodeDownloadedArtifactChunk{}, ErrRemoteWorkspaceUnavailable
	}
	var transferred NodeFileTransferResult
	if json.Unmarshal([]byte(result.ContentForLLM()), &transferred) != nil ||
		transferred.State != "committed" || transferred.ArtifactRef == "" {
		return RemoteWorkspaceArtifact{}, NodeDownloadedArtifactChunk{}, ErrRemoteWorkspaceUnavailable
	}
	chunk, err := router.source.ReadDownloadedArtifactRange(
		ctx,
		owner,
		transferred.ArtifactRef,
		offset,
		limit,
	)
	if err != nil {
		return RemoteWorkspaceArtifact{}, NodeDownloadedArtifactChunk{}, ErrRemoteWorkspaceUnavailable
	}
	if retainedFound {
		contentType := chunk.ContentType
		if contentType == "" {
			contentType = "application/octet-stream"
		}
		artifact = RemoteWorkspaceArtifact{
			Target: binding.config.Target, Ref: artifactRef, Name: chunk.Filename,
			State: "available", Size: chunk.Size, SHA256: chunk.SHA256, ContentType: contentType,
		}
	} else if chunk.Size != artifact.Size || chunk.SHA256 != artifact.SHA256 {
		return RemoteWorkspaceArtifact{}, NodeDownloadedArtifactChunk{}, ErrRemoteWorkspaceUnavailable
	}
	return artifact, chunk, nil
}

func (router *RemoteWorkspaceArtifactRouter) resolve(
	ctx context.Context,
	startCtx context.Context,
	workspaceAlias string,
	jobInvocationID string,
	artifactRef string,
) (RemoteWorkspaceArtifact, remoteWorkspaceJobAuthority, string, error) {
	if router == nil || router.jobs == nil || router.source == nil ||
		strings.TrimSpace(jobInvocationID) == "" || strings.TrimSpace(artifactRef) == "" {
		return RemoteWorkspaceArtifact{}, remoteWorkspaceJobAuthority{}, "", ErrRemoteWorkspaceUnavailable
	}
	binding, ok := router.jobs.byAlias[workspaceAlias]
	if !ok || !binding.allowJobs {
		return RemoteWorkspaceArtifact{}, remoteWorkspaceJobAuthority{}, "", ErrRemoteWorkspaceUnavailable
	}
	job, err := router.jobs.resolveRemoteWorkspaceJob(startCtx, binding)
	if err != nil {
		return RemoteWorkspaceArtifact{}, remoteWorkspaceJobAuthority{}, "", err
	}
	prepared, err := router.jobs.prepareRemoteWorkspaceJobInvocation(
		binding,
		job,
		nodes.JobCommandArtifacts,
		map[string]any{"job_id": job.id},
	)
	if err != nil {
		return RemoteWorkspaceArtifact{}, remoteWorkspaceJobAuthority{}, "", err
	}
	revision, ok := prepared["discovery_revision"].(string)
	if !ok || revision == "" {
		return RemoteWorkspaceArtifact{}, remoteWorkspaceJobAuthority{}, "", ErrRemoteWorkspaceUnavailable
	}
	queryCtx := toolshared.WithToolCallID(
		bindRemoteWorkspaceInvocationIdentity(ctx, binding),
		stableNodeInvocationID("coding_artifact_query", jobInvocationID),
	)
	retained, retainedFound, retainedErr := LookupNodeInvocationByCurrentCall(queryCtx, router.source)
	var result *toolshared.ToolResult
	if retainedErr != nil {
		return RemoteWorkspaceArtifact{}, remoteWorkspaceJobAuthority{}, "", ErrRemoteWorkspaceUnavailable
	}
	if retainedFound {
		var retainedInput struct {
			JobID string `json:"job_id"`
		}
		if retained.Target != binding.config.Target || retained.Plan.NodeID != job.nodeID ||
			retained.Plan.JobProfile != job.jobProfile || retained.Plan.Command != nodes.JobCommandArtifacts ||
			json.Unmarshal(retained.Plan.Input, &retainedInput) != nil || retainedInput.JobID != job.id {
			return RemoteWorkspaceArtifact{}, remoteWorkspaceJobAuthority{}, "", ErrRemoteWorkspaceUnavailable
		}
		status := &NodeStatusTool{runtime: router.jobs.runtime}
		result = status.Execute(queryCtx, map[string]any{"invocation_id": retained.Plan.InvocationID})
	} else {
		result = router.jobs.invoke.execute(queryCtx, prepared, false)
	}
	if result == nil || result.IsError {
		return RemoteWorkspaceArtifact{}, remoteWorkspaceJobAuthority{}, "", ErrRemoteWorkspaceUnavailable
	}
	var wire nodeInvokeResult
	if json.Unmarshal([]byte(result.ContentForLLM()), &wire) != nil || wire.State != "succeeded" {
		return RemoteWorkspaceArtifact{}, remoteWorkspaceJobAuthority{}, "", ErrRemoteWorkspaceUnavailable
	}
	var payload struct {
		Artifacts []struct {
			Name        string `json:"name"`
			State       string `json:"state"`
			ArtifactRef string `json:"artifact_ref"`
			Size        int64  `json:"size"`
			SHA256      string `json:"sha256"`
			FailureCode string `json:"failure_code"`
		} `json:"artifacts"`
	}
	if json.Unmarshal(wire.Result, &payload) != nil {
		return RemoteWorkspaceArtifact{}, remoteWorkspaceJobAuthority{}, "", ErrRemoteWorkspaceUnavailable
	}
	for _, candidate := range payload.Artifacts {
		if candidate.ArtifactRef != artifactRef {
			continue
		}
		if candidate.State != "available" || candidate.FailureCode != "" || candidate.Name == "" ||
			candidate.Size < 0 || candidate.Size > nodes.MaxTransferArtifactBytes ||
			len(candidate.SHA256) != 64 {
			return RemoteWorkspaceArtifact{}, remoteWorkspaceJobAuthority{}, "", ErrRemoteWorkspaceUnavailable
		}
		return RemoteWorkspaceArtifact{
			Target: binding.config.Target, Ref: candidate.ArtifactRef, Name: candidate.Name,
			State: candidate.State, Size: candidate.Size, SHA256: candidate.SHA256,
			ContentType: "application/octet-stream",
		}, job, revision, nil
	}
	return RemoteWorkspaceArtifact{}, remoteWorkspaceJobAuthority{}, "", fmt.Errorf(
		"%w: artifact is not owned by the job",
		ErrRemoteWorkspaceUnavailable,
	)
}
