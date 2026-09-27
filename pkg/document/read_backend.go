package document

import (
	"math"
	"unicode/utf8"
)

const (
	PopplerBackendName    = "poppler"
	PopplerBackendVersion = "24.02.0"
)

type readBackend interface {
	Extract([]byte, WorkerRequest) backendRead
	Render([]byte, WorkerRequest) backendRead
}

type backendRead struct {
	State      State
	Extraction *ExtractionFacts
	Rendering  *RenderingFacts
	Artifacts  []WorkerArtifact
	Failure    *Failure
}

func failedRead(code FailureCode, message string) backendRead {
	return backendRead{
		State:   StateFailed,
		Failure: &Failure{Code: code, Message: message},
	}
}

func popplerIdentity() BackendIdentity {
	return nativeBackendIdentity(PopplerBackendName)
}

func validPopplerIdentity(identity BackendIdentity) bool {
	return identity == popplerIdentity() || identity == legacyPopplerProductionIdentity()
}

func legacyPopplerProductionIdentity() BackendIdentity {
	identity := popplerIdentity()
	identity.Role = "production"
	return identity
}

func workerArtifactRef(operationID, name string) string {
	return "document-artifact://" + operationID + "/" + name
}

func boundExtractedPageText(text string, remaining int, hasLaterPage bool) (string, int, bool) {
	characters := utf8.RuneCountInString(text)
	truncated := characters > remaining || (characters == remaining && hasLaterPage)
	if characters > remaining {
		return truncateRunes(text, remaining), remaining, true
	}
	return text, characters, truncated
}

func truncateRunes(value string, maximum int) string {
	if maximum <= 0 {
		return ""
	}
	runes := []rune(value)
	if len(runes) <= maximum {
		return value
	}
	return string(runes[:maximum])
}

func boundedPageDimensions(widthPoints, heightPoints float64, dpi, maxDimension, rotation int) (int, int, *Failure) {
	if widthPoints <= 0 || heightPoints <= 0 || math.IsNaN(widthPoints) || math.IsNaN(heightPoints) ||
		math.IsInf(widthPoints, 0) || math.IsInf(heightPoints, 0) ||
		(rotation != 0 && rotation != 90 && rotation != 180 && rotation != 270) {
		return 0, 0, &Failure{Code: FailureArtifactInvalid, Message: "document page dimensions are unavailable"}
	}
	widthPixels := math.Ceil(widthPoints * float64(dpi) / 72)
	heightPixels := math.Ceil(heightPoints * float64(dpi) / 72)
	if math.IsNaN(widthPixels) || math.IsNaN(heightPixels) || math.IsInf(widthPixels, 0) ||
		math.IsInf(heightPixels, 0) || widthPixels <= 0 || heightPixels <= 0 ||
		widthPixels > float64(maxDimension) || heightPixels > float64(maxDimension) {
		return 0, 0, &Failure{Code: FailureRenderLimit, Message: "document page dimensions exceed the render limit"}
	}
	width := int(widthPixels)
	height := int(heightPixels)
	if rotation == 90 || rotation == 270 {
		width, height = height, width
	}
	return width, height, nil
}
