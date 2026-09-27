package document

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/klippa-app/go-pdfium/requests"
)

func TestPortablePDFiumFactoryHasNoHostFilesystemView(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("testdata", "text.pdf"))
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	hostPath := filepath.Join(t.TempDir(), "host-visible.pdf")
	if err = os.WriteFile(hostPath, data, 0o600); err != nil {
		t.Fatalf("write host fixture: %v", err)
	}

	pool, err := newPortablePDFiumPool(t.Context())
	if err != nil {
		t.Fatalf("initialize portable PDFium: %v", err)
	}
	t.Cleanup(func() {
		if err := pool.Close(); err != nil {
			t.Errorf("close portable PDFium pool: %v", err)
		}
	})
	instance, err := pool.GetInstanceWithContext(t.Context())
	if err != nil {
		t.Fatalf("get portable PDFium instance: %v", err)
	}
	t.Cleanup(func() {
		if err := instance.Close(); err != nil {
			t.Errorf("close portable PDFium instance: %v", err)
		}
	})

	if document, openErr := instance.OpenDocument(&requests.OpenDocument{FilePath: &hostPath}); openErr == nil {
		_, _ = instance.FPDF_CloseDocument(&requests.FPDF_CloseDocument{Document: document.Document})
		t.Fatalf("portable PDFium opened ambient host path %q", hostPath)
	}
	document, err := instance.OpenDocument(&requests.OpenDocument{File: &data})
	if err != nil {
		t.Fatalf("open fixture bytes: %v", err)
	}
	if _, err = instance.FPDF_CloseDocument(&requests.FPDF_CloseDocument{Document: document.Document}); err != nil {
		t.Fatalf("close fixture: %v", err)
	}
}

func TestPortablePDFiumIdentityPinsQualifiedRuntime(t *testing.T) {
	identity := pdfiumWASMIdentity()
	if identity.Name != PDFiumWASMBackendName || identity.Version != PDFiumWASMBackendVersion ||
		identity.Package != PDFiumWASMBackendPackage || identity.PackageRevision != PDFiumWASMBackendRevision ||
		identity.ArtifactSHA256 != PDFiumWASMArtifactSHA256 || identity.Runtime != PDFiumWASMRuntime ||
		identity.RuntimeVersion != PDFiumWASMRuntimeVersion || identity.IsolationMode != PDFiumWASMIsolationMode {
		t.Fatalf("portable identity = %#v", identity)
	}
}
