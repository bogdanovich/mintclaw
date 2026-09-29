package document

import (
	"context"
	"io"

	"github.com/klippa-app/go-pdfium"
	"github.com/klippa-app/go-pdfium/webassembly"
	"github.com/tetratelabs/wazero"
	"github.com/tetratelabs/wazero/api"
	"github.com/tetratelabs/wazero/experimental"
)

const (
	PDFiumWASMBackendName       = "pdfium-wasm"
	PDFiumWASMBackendVersion    = "8044"
	PDFiumWASMBackendPackage    = "github.com/klippa-app/go-pdfium"
	PDFiumWASMBackendRevision   = "v1.20.0"
	PDFiumWASMRuntime           = "github.com/tetratelabs/wazero"
	PDFiumWASMRuntimeVersion    = "v1.12.0"
	PDFiumWASMArtifactSHA256    = "f651270c675cac90702b762f4b95d2b34e365cdb374065e40af016f4f0f304ea"
	PDFiumWASMIsolationMode     = "wazero_empty_fs+one_shot_document_worker"
	portableWASMMemoryLimitPage = 2_560
)

type portablePDFiumFactory func(context.Context) (pdfium.Pool, error)

func pdfiumWASMIdentity() BackendIdentity {
	return BackendIdentity{
		Name:            PDFiumWASMBackendName,
		Version:         PDFiumWASMBackendVersion,
		Package:         PDFiumWASMBackendPackage,
		PackageRevision: PDFiumWASMBackendRevision,
		ArtifactSHA256:  PDFiumWASMArtifactSHA256,
		Runtime:         PDFiumWASMRuntime,
		RuntimeVersion:  PDFiumWASMRuntimeVersion,
		Role:            "portable_read_backend",
		IsolationMode:   PDFiumWASMIsolationMode,
	}
}

func newPortablePDFiumPool(ctx context.Context) (pdfium.Pool, error) {
	features := api.CoreFeaturesV2 | experimental.CoreFeaturesExceptionHandling
	runtimeConfig := wazero.NewRuntimeConfig().
		WithCoreFeatures(features).
		WithMemoryLimitPages(portableWASMMemoryLimitPage).
		WithCloseOnContextDone(true)
	return webassembly.Init(webassembly.Config{
		Context:       ctx,
		MinIdle:       0,
		MaxIdle:       1,
		MaxTotal:      1,
		FSConfig:      wazero.NewFSConfig(),
		RuntimeConfig: runtimeConfig,
		Stdout:        io.Discard,
		Stderr:        io.Discard,
		ReuseWorkers:  false,
	})
}
