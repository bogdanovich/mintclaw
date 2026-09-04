package workspace

import "slices"

func (result StatusResult) Clone() StatusResult {
	result.Snapshot.ChangedPaths = slices.Clone(result.Snapshot.ChangedPaths)
	if result.Provenance != nil {
		provenance := *result.Provenance
		provenance.Paths = slices.Clone(provenance.Paths)
		result.Provenance = &provenance
	}
	return result
}

func (result DiffResult) Clone() DiffResult {
	result.Files = slices.Clone(result.Files)
	for fileIndex := range result.Files {
		result.Files[fileIndex].Hunks = slices.Clone(result.Files[fileIndex].Hunks)
		for hunkIndex := range result.Files[fileIndex].Hunks {
			result.Files[fileIndex].Hunks[hunkIndex].Lines = slices.Clone(
				result.Files[fileIndex].Hunks[hunkIndex].Lines,
			)
		}
	}
	if result.Provenance != nil {
		provenance := *result.Provenance
		provenance.Paths = slices.Clone(provenance.Paths)
		result.Provenance = &provenance
	}
	return result
}
