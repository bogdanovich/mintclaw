package eval

import (
	"context"
	"errors"
	"os"

	"github.com/bogdanovich/mintclaw/pkg/seahorse"
)

const privateSQLitePath = "workspace/sessions/seahorse.db"

type SQLiteSurfaceObservation struct {
	Scope       string `json:"scope"`
	Code        string `json:"code"`
	Rows        int    `json:"rows"`
	TextValues  int    `json:"text_values"`
	IndexValues int    `json:"index_values"`
	ValueBytes  int    `json:"value_bytes"`
}

func (observation *SQLiteSurfaceObservation) valid() bool {
	if observation == nil {
		return true
	}
	return observation.Scope == "seahorse_text_and_raw_json_escapes_v1" &&
		(observation.Code == "observed" || observation.Code == "unavailable" || observation.Code == "leak_observed") &&
		observation.Rows >= 0 && observation.Rows <= seahorse.SnapshotMaxRows &&
		observation.TextValues >= 0 && observation.TextValues <= seahorse.SnapshotMaxRows*64+1024 &&
		observation.IndexValues >= 0 && observation.IndexValues <= seahorse.SnapshotMaxRows*64+1024 &&
		observation.TextValues+observation.IndexValues <= seahorse.SnapshotMaxRows*64+1024 &&
		observation.ValueBytes >= 0 && observation.ValueBytes <= seahorse.SnapshotMaxBytes
}

func sqliteSidecarsAbsent(root *os.Root) bool {
	for _, suffix := range []string{"-wal", "-shm", "-journal"} {
		if _, err := root.Lstat(privateSQLitePath + suffix); !errors.Is(err, os.ErrNotExist) {
			return false
		}
	}
	return true
}

func observePrivateSQLite(
	ctx context.Context, root *os.Root, before os.FileInfo, image []byte, sidecarsBefore bool, scan *privateContentScan,
) SQLiteSurfaceObservation {
	result := SQLiteSurfaceObservation{Scope: "seahorse_text_and_raw_json_escapes_v1", Code: "unavailable"}
	if scan.rawEscapes(ctx, image) {
		if !scan.uncertain {
			result.Code = "leak_observed"
		}
		return result
	}
	if !sidecarsBefore || !sqliteSidecarsAbsent(root) {
		return result
	}
	stats, err := seahorse.InspectSnapshot(ctx, image, func(value []byte, kind seahorse.SnapshotValueKind) error {
		scan.uncertain = false
		var matched bool
		if kind == seahorse.SnapshotIndexBlob {
			matched = scan.rawEscapes(ctx, value)
		} else {
			matched = scan.text(string(value), 0)
		}
		if matched {
			if scan.uncertain {
				return errNativeEvidence
			}
			return ErrPrivateEvidence
		}
		return nil
	})
	result.Rows, result.TextValues, result.IndexValues, result.ValueBytes = stats.Rows, stats.TextValues, stats.BinaryValues, stats.ValueBytes
	if errors.Is(err, ErrPrivateEvidence) {
		result.Code = "leak_observed"
		return result
	}
	after, statErr := root.Lstat(privateSQLitePath)
	if err == nil && statErr == nil && sameCensusFile(before, after) && int64(len(image)) == after.Size() &&
		sqliteSidecarsAbsent(root) && ctx.Err() == nil {
		result.Code = "observed"
	}
	return result
}
