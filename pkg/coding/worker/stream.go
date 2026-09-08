package worker

import (
	"bufio"
	"errors"
	"fmt"
	"io"
)

const wireReadBufferBytes = 64 << 10

type wireReadResult struct {
	record Record
	raw    []byte
	err    error
}

type wireReader struct {
	reader *bufio.Reader
}

func newWireReader(reader io.Reader) (*wireReader, error) {
	if reader == nil {
		return nil, errors.New("coding worker wire reader is required")
	}
	return &wireReader{reader: bufio.NewReaderSize(reader, wireReadBufferBytes)}, nil
}

func (reader *wireReader) read() wireReadResult {
	raw, err := reader.readLine()
	if err != nil {
		return wireReadResult{raw: raw, err: err}
	}
	record, err := Decode(raw)
	return wireReadResult{record: record, raw: raw, err: err}
}

func (reader *wireReader) readLine() ([]byte, error) {
	line := make([]byte, 0, wireReadBufferBytes)
	for {
		fragment, err := reader.reader.ReadSlice('\n')
		if len(line)+len(fragment) > MaxRecordBytes+1 {
			reader.discardLine(err)
			return nil, ErrRecordTooLarge
		}
		line = append(line, fragment...)
		switch {
		case err == nil:
			line = line[:len(line)-1]
			if len(line) > 0 && line[len(line)-1] == '\r' {
				line = line[:len(line)-1]
			}
			if len(line) > MaxRecordBytes {
				return nil, ErrRecordTooLarge
			}
			return line, nil
		case errors.Is(err, bufio.ErrBufferFull):
			continue
		case errors.Is(err, io.EOF) && len(line) != 0:
			if len(line) > MaxRecordBytes {
				return nil, ErrRecordTooLarge
			}
			return line, nil
		default:
			return line, err
		}
	}
}

func (reader *wireReader) discardLine(readErr error) {
	for errors.Is(readErr, bufio.ErrBufferFull) {
		_, readErr = reader.reader.ReadSlice('\n')
	}
}

func writeWireRecord(writer io.Writer, record Record) (int, error) {
	if writer == nil {
		return 0, errors.New("coding worker wire writer is required")
	}
	encoded, err := Encode(record)
	if err != nil {
		return 0, err
	}
	encoded = append(encoded, '\n')
	written := 0
	for written < len(encoded) {
		count, writeErr := writer.Write(encoded[written:])
		if count < 0 || count > len(encoded)-written {
			return written, fmt.Errorf("coding worker wire writer returned an invalid byte count")
		}
		written += count
		if writeErr != nil {
			return written, writeErr
		}
		if count == 0 {
			return written, io.ErrNoProgress
		}
	}
	return written, nil
}

func cloneRecord(record Record) Record {
	record.Params = append([]byte(nil), record.Params...)
	record.Result = append([]byte(nil), record.Result...)
	record.Payload = append([]byte(nil), record.Payload...)
	if record.Error != nil {
		protocolError := *record.Error
		protocolError.Details = append([]byte(nil), record.Error.Details...)
		record.Error = &protocolError
	}
	if record.OK != nil {
		ok := *record.OK
		record.OK = &ok
	}
	return record
}
