// Package browserworker owns the credential service's private browser channel.
// No type is a model tool, ordinary browser handle, or deployment certificate.
package browserworker

import (
	"encoding/base64"
	"encoding/binary"
	"errors"
	"io"
	"strings"
	"unicode/utf8"

	"github.com/bogdanovich/mintclaw/pkg/credentialbroker"
)

const maxFrameBytes = 32768

var errUnavailable = errors.New("private credential browser is unavailable")

type packet struct {
	kind byte
	id   uint32
	body []byte
}

func readPacket(reader io.Reader) (packet, error) {
	var header [4]byte
	if _, err := io.ReadFull(reader, header[:]); err != nil {
		return packet{}, errUnavailable
	}
	size := binary.BigEndian.Uint32(header[:])
	if size < 5 || size > maxFrameBytes {
		return packet{}, errUnavailable
	}
	buffer := make([]byte, size)
	if _, err := io.ReadFull(reader, buffer); err != nil {
		clear(buffer)
		return packet{}, errUnavailable
	}
	return packet{buffer[0], binary.BigEndian.Uint32(buffer[1:5]), buffer[5:]}, nil
}

func encodePacket(kind byte, id uint32, body []byte) ([]byte, error) {
	if len(body)+5 > maxFrameBytes {
		return nil, errUnavailable
	}
	buffer := make([]byte, 9+len(body))
	binary.BigEndian.PutUint32(buffer[:4], uint32(5+len(body)))
	buffer[4] = kind
	binary.BigEndian.PutUint32(buffer[5:9], id)
	copy(buffer[9:], body)
	return buffer, nil
}

func addString(buffer []byte, value string, limit int) ([]byte, error) {
	if len(value) > limit || !utf8.ValidString(value) || strings.ContainsAny(value, "\x00\r\n") {
		return nil, errUnavailable
	}
	return addBytes(buffer, []byte(value), limit)
}

func addBytes(buffer, value []byte, limit int) ([]byte, error) {
	if len(value) > limit || len(buffer)+2+len(value) > maxFrameBytes-5 {
		return nil, errUnavailable
	}
	buffer = binary.BigEndian.AppendUint16(buffer, uint16(len(value)))
	return append(buffer, value...), nil
}

func encodeConfiguration(config launchConfig, flow credentialbroker.LoginFlow, origins []string,
	remaining uint32,
) ([]byte, error) {
	if remaining == 0 || remaining > 20*60*60*1000 || len(origins) == 0 || len(origins) > 16 {
		return nil, errUnavailable
	}
	pin, err := base64.StdEncoding.DecodeString(config.proxyPin)
	if err != nil || len(pin) != 32 || base64.StdEncoding.EncodeToString(pin) != config.proxyPin {
		return nil, errUnavailable
	}
	buffer := binary.BigEndian.AppendUint32(nil, remaining)
	for _, field := range []struct {
		value string
		limit int
	}{
		{config.executablePath, 4096},
		{config.proxyServer, 256},
		{config.proxyPin, 44},
		{flow.NavigationURL, 2048},
		{flow.EntryOrigin, 2048},
		{flow.FormID, 2048},
		{flow.UsernameID, 2048},
		{flow.PasswordID, 2048},
		{flow.UsernameType, 2048},
		{flow.SubmitURL, 2048},
		{flow.SuccessURL, 2048},
		{flow.SuccessMarkerID, 2048},
		{flow.HandoffURL, 2048},
		{flow.HandoffMarkerID, 2048},
	} {
		buffer, err = addString(buffer, field.value, field.limit)
		if err != nil {
			return nil, errUnavailable
		}
	}
	buffer = binary.BigEndian.AppendUint32(buffer, uint32(len(origins)))
	for _, origin := range origins {
		buffer, err = addString(buffer, origin, 2048)
		if err != nil {
			return nil, errUnavailable
		}
	}
	return buffer, nil
}

var callbackOperations = [...]string{
	"",
	"navigate",
	"network",
	"prepare",
	"retrieve",
	"inject",
	"submit",
	"outcome",
	"lease",
}

func decodeCheck(body []byte) (string, string, error) {
	if len(body) < 4 || body[0] == 0 || int(body[0]) >= len(callbackOperations) {
		return "", "", errUnavailable
	}
	size := int(binary.BigEndian.Uint16(body[1:3]))
	if size == 0 || size > 2048 || len(body) != 3+size || !utf8.Valid(body[3:]) {
		return "", "", errUnavailable
	}
	origin := string(body[3:])
	if strings.ContainsAny(origin, "\x00\r\n") {
		return "", "", errUnavailable
	}
	return callbackOperations[body[0]], origin, nil
}
