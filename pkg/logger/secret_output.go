package logger

import (
	"io"

	"github.com/bogdanovich/mintclaw/internal/secretvalues"
)

// Filter the complete serialized event before console, file, or other logger
// outputs receive it, including values nested under ordinary field names.
type secretOutputWriter struct{ destination io.Writer }

func (writer secretOutputWriter) Write(data []byte) (int, error) {
	filtered := secretvalues.RedactRecord(data)
	count, err := writer.destination.Write(filtered)
	if err != nil {
		return 0, err
	}
	if count != len(filtered) {
		return 0, io.ErrShortWrite
	}
	return len(data), nil
}

func loggerOutput() io.Writer {
	return secretOutputWriter{destination: io.MultiWriter(writers...)}
}
