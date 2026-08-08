package control

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/bogdanovich/mintclaw/pkg/nodes"
	"github.com/bogdanovich/mintclaw/pkg/nodes/internal/jsonstrict"
)

const (
	SchemaVersion = 1
	MaxFrameBytes = 64 * 1024
)

type Kind string

const (
	KindUpdate Kind = "update"
	KindStatus Kind = "status"
	KindCancel Kind = "cancel"
	KindHealth Kind = "health"
)

type ExecutionIdentity struct {
	InvocationID  string `json:"invocation_id"`
	ExecutionID   string `json:"execution_id"`
	PlanHash      string `json:"plan_hash"`
	CatalogHash   string `json:"catalog_hash"`
	AuthorityHash string `json:"authority_hash"`
}

type UpdateRequest struct {
	Identity               ExecutionIdentity `json:"identity"`
	Profile                string            `json:"profile"`
	ReleaseAlias           string            `json:"release_alias"`
	ExpectedManifestSHA256 string            `json:"expected_manifest_sha256"`
	ExpectedArtifactSHA256 string            `json:"expected_artifact_sha256"`
	ExpiresAt              int64             `json:"expires_at"`
}

type Request struct {
	SchemaVersion int                `json:"schema_version"`
	Kind          Kind               `json:"kind"`
	RequestID     string             `json:"request_id"`
	Update        *UpdateRequest     `json:"update,omitempty"`
	Identity      *ExecutionIdentity `json:"identity,omitempty"`
}

type Observation struct {
	Phase               string `json:"phase"`
	RequestedRelease    string `json:"requested_release,omitempty"`
	PreviousRelease     string `json:"previous_release,omitempty"`
	InstalledVersion    string `json:"installed_version,omitempty"`
	ActivationAttempted bool   `json:"activation_attempted"`
	SuccessorVerified   bool   `json:"successor_verified"`
	RollbackAttempted   bool   `json:"rollback_attempted"`
	RollbackVerified    bool   `json:"rollback_verified"`
	FailureCode         string `json:"failure_code,omitempty"`
}

type Response struct {
	SchemaVersion int         `json:"schema_version"`
	RequestID     string      `json:"request_id"`
	Observation   Observation `json:"observation"`
	ErrorCode     string      `json:"error_code,omitempty"`
}

type Health struct {
	SchemaVersion int    `json:"schema_version"`
	Kind          Kind   `json:"kind"`
	NodeID        string `json:"node_id"`
	Version       string `json:"version"`
	Platform      string `json:"platform"`
	Architecture  string `json:"architecture"`
	CatalogHash   string `json:"catalog_hash"`
}

var (
	boundedToken  = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,127}$`)
	nodeIDPattern = regexp.MustCompile(`^node_[a-z2-7]{52}$`)
)

type Incoming struct {
	Request *Request
	Health  *Health
}

func (identity ExecutionIdentity) Validate() error {
	if !bounded(identity.InvocationID, nodes.MaxIDLength) || !bounded(identity.ExecutionID, nodes.MaxIDLength) ||
		!digest(identity.PlanHash) || !digest(identity.CatalogHash) || !digest(identity.AuthorityHash) {
		return errors.New("invalid coordinator execution identity")
	}
	return nil
}

func (request UpdateRequest) Validate(now time.Time) error {
	if err := request.Identity.Validate(); err != nil {
		return err
	}
	if !boundedToken.MatchString(request.Profile) || !boundedToken.MatchString(request.ReleaseAlias) ||
		!digest(request.ExpectedManifestSHA256) || !digest(request.ExpectedArtifactSHA256) ||
		request.ExpiresAt <= now.Unix() || request.ExpiresAt > now.Add(30*time.Minute).Unix() {
		return errors.New("invalid coordinator update request")
	}
	return nil
}

func (request Request) Validate(now time.Time) error {
	if request.SchemaVersion != SchemaVersion || !boundedToken.MatchString(request.RequestID) {
		return errors.New("invalid coordinator request envelope")
	}
	switch request.Kind {
	case KindUpdate:
		if request.Update == nil || request.Identity != nil {
			return errors.New("invalid coordinator update envelope")
		}
		return request.Update.Validate(now)
	case KindStatus, KindCancel:
		if request.Update != nil || request.Identity == nil {
			return errors.New("invalid coordinator identity envelope")
		}
		return request.Identity.Validate()
	default:
		return errors.New("unsupported coordinator request")
	}
}

func (response Response) Validate() error {
	if response.SchemaVersion != SchemaVersion || !boundedToken.MatchString(response.RequestID) ||
		(response.ErrorCode != "" && !boundedToken.MatchString(response.ErrorCode)) {
		return errors.New("invalid coordinator response envelope")
	}
	return response.Observation.Validate()
}

func (observation Observation) Validate() error {
	if !boundedToken.MatchString(observation.Phase) ||
		(observation.RequestedRelease != "" && !bounded(observation.RequestedRelease, 128)) ||
		(observation.PreviousRelease != "" && !bounded(observation.PreviousRelease, 128)) ||
		(observation.InstalledVersion != "" && !bounded(observation.InstalledVersion, 128)) ||
		(observation.FailureCode != "" && !boundedToken.MatchString(observation.FailureCode)) {
		return errors.New("invalid coordinator observation")
	}
	return nil
}

func (health Health) Validate() error {
	if health.SchemaVersion != SchemaVersion || health.Kind != KindHealth ||
		errInvalidNodeID(health.NodeID) || !bounded(health.Version, 128) ||
		(health.Platform != "linux" && health.Platform != "darwin") ||
		(health.Architecture != "amd64" && health.Architecture != "arm64") || !digest(health.CatalogHash) {
		return errors.New("invalid coordinator health observation")
	}
	return nil
}

func errInvalidNodeID(value string) bool {
	return !nodeIDPattern.MatchString(value) || nodes.ID(value).Validate() != nil
}

func bounded(value string, maximum int) bool {
	return value != "" && len(value) <= maximum && value == strings.TrimSpace(value)
}

func digest(value string) bool {
	decoded, err := hex.DecodeString(value)
	return err == nil && len(decoded) == sha256.Size && hex.EncodeToString(decoded) == value
}

type Codec struct {
	reader io.Reader
	writer io.Writer
	mu     sync.Mutex
}

func NewCodec(reader io.Reader, writer io.Writer) (*Codec, error) {
	if reader == nil || writer == nil {
		return nil, errors.New("coordinator control codec requires both directions")
	}
	return &Codec{reader: reader, writer: writer}, nil
}

func (codec *Codec) ReadRequest(now time.Time) (Request, error) {
	data, err := codec.readFrame()
	if err != nil {
		return Request{}, err
	}
	var request Request
	if err = decode(data, &request); err != nil {
		return Request{}, err
	}
	return request, request.Validate(now)
}

func (codec *Codec) ReadIncoming(now time.Time) (Incoming, error) {
	data, err := codec.readFrame()
	if err != nil {
		return Incoming{}, err
	}
	var envelope struct {
		Kind Kind `json:"kind"`
	}
	if err = json.Unmarshal(data, &envelope); err != nil {
		return Incoming{}, errors.New("decode coordinator control envelope")
	}
	if envelope.Kind == KindHealth {
		var health Health
		if err = decode(data, &health); err != nil {
			return Incoming{}, err
		}
		if err = health.Validate(); err != nil {
			return Incoming{}, err
		}
		return Incoming{Health: &health}, nil
	}
	var request Request
	if err = decode(data, &request); err != nil {
		return Incoming{}, err
	}
	if err = request.Validate(now); err != nil {
		return Incoming{}, err
	}
	return Incoming{Request: &request}, nil
}

func (codec *Codec) ReadResponse() (Response, error) {
	data, err := codec.readFrame()
	if err != nil {
		return Response{}, err
	}
	var response Response
	if err = decode(data, &response); err != nil {
		return Response{}, err
	}
	return response, response.Validate()
}

func (codec *Codec) ReadHealth() (Health, error) {
	data, err := codec.readFrame()
	if err != nil {
		return Health{}, err
	}
	var health Health
	if err = decode(data, &health); err != nil {
		return Health{}, err
	}
	return health, health.Validate()
}

func (codec *Codec) WriteRequest(request Request, now time.Time) error {
	if err := request.Validate(now); err != nil {
		return err
	}
	return codec.writeFrame(request)
}

func (codec *Codec) WriteResponse(response Response) error {
	if err := response.Validate(); err != nil {
		return err
	}
	return codec.writeFrame(response)
}

func (codec *Codec) WriteHealth(health Health) error {
	if err := health.Validate(); err != nil {
		return err
	}
	return codec.writeFrame(health)
}

func (codec *Codec) readFrame() ([]byte, error) {
	var header [4]byte
	if _, err := io.ReadFull(codec.reader, header[:]); err != nil {
		return nil, err
	}
	size := binary.BigEndian.Uint32(header[:])
	if size == 0 || size > MaxFrameBytes {
		return nil, errors.New("coordinator control frame exceeds its bound")
	}
	data := make([]byte, size)
	if _, err := io.ReadFull(codec.reader, data); err != nil {
		return nil, err
	}
	return data, nil
}

func (codec *Codec) writeFrame(value any) error {
	data, err := json.Marshal(value)
	if err != nil || len(data) == 0 || len(data) > MaxFrameBytes {
		return errors.New("encode bounded coordinator control frame")
	}
	var header [4]byte
	binary.BigEndian.PutUint32(header[:], uint32(len(data)))
	codec.mu.Lock()
	defer codec.mu.Unlock()
	if err = writeFull(codec.writer, header[:]); err != nil {
		return err
	}
	return writeFull(codec.writer, data)
}

func writeFull(writer io.Writer, data []byte) error {
	for len(data) > 0 {
		written, err := writer.Write(data)
		if err != nil {
			return err
		}
		if written <= 0 {
			return io.ErrShortWrite
		}
		data = data[written:]
	}
	return nil
}

func decode(data []byte, destination any) error {
	if _, err := jsonstrict.Decode(data); err != nil {
		return err
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(destination); err != nil {
		return err
	}
	if err := decoder.Decode(new(any)); !errors.Is(err, io.EOF) {
		return errors.New("coordinator control frame contains trailing data")
	}
	return nil
}

func NewRequestID() (string, error) {
	var value [16]byte
	if _, err := rand.Read(value[:]); err != nil {
		return "", fmt.Errorf("create coordinator request identity: %w", err)
	}
	return "request_" + hex.EncodeToString(value[:]), nil
}
