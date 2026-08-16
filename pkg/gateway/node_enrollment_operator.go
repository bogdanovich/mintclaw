package gateway

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/bogdanovich/mintclaw/pkg/nodes"
)

const (
	nodeEnrollmentOperatorPath      = "/nodes/v1/enrollment-offers"
	nodeEnrollmentOperatorBodyLimit = 8 * 1024
)

type nodeEnrollmentOperatorHandler struct {
	token  string
	offers *nodes.EnrollmentOfferManager
}

func newNodeEnrollmentOperatorHandler(
	token string,
	offers *nodes.EnrollmentOfferManager,
) *nodeEnrollmentOperatorHandler {
	return &nodeEnrollmentOperatorHandler{token: strings.TrimSpace(token), offers: offers}
}

func (handler *nodeEnrollmentOperatorHandler) ServeHTTP(writer http.ResponseWriter, request *http.Request) {
	writer.Header().Set("Cache-Control", "no-store")
	if handler == nil || handler.offers == nil || !handler.authenticate(request) {
		writeNodeEnrollmentError(writer, http.StatusUnauthorized, "UNAUTHORIZED")
		return
	}
	if request.Method != http.MethodPost {
		writer.Header().Set("Allow", http.MethodPost)
		writeNodeEnrollmentError(writer, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED")
		return
	}
	request.Body = http.MaxBytesReader(writer, request.Body, nodeEnrollmentOperatorBodyLimit)
	decoder := json.NewDecoder(request.Body)
	decoder.DisallowUnknownFields()
	var input nodes.EnrollmentOfferRequest
	if err := decoder.Decode(&input); err != nil {
		writeNodeEnrollmentError(writer, http.StatusBadRequest, "INVALID_REQUEST")
		return
	}
	if err := decoder.Decode(new(any)); !errors.Is(err, io.EOF) {
		writeNodeEnrollmentError(writer, http.StatusBadRequest, "INVALID_REQUEST")
		return
	}
	if input.TTLSeconds < 0 || input.TTLSeconds > int(nodes.MaxEnrollmentOfferTTL/time.Second) {
		writeNodeEnrollmentError(writer, http.StatusBadRequest, "INVALID_REQUEST")
		return
	}
	offer, err := handler.offers.Issue(
		input.Endpoint,
		input.SPKISHA256,
		time.Duration(input.TTLSeconds)*time.Second,
	)
	if err != nil {
		status := http.StatusBadRequest
		code := "INVALID_REQUEST"
		if errors.Is(err, nodes.ErrEnrollmentOfferBusy) {
			status = http.StatusTooManyRequests
			code = "CAPACITY_REACHED"
		} else if errors.Is(err, nodes.ErrEnrollmentInvalidated) {
			status = http.StatusServiceUnavailable
			code = "OFFER_UNAVAILABLE"
		}
		writeNodeEnrollmentError(writer, status, code)
		return
	}
	uri, err := nodes.EncodeEnrollmentOfferURI(offer)
	if err != nil {
		writeNodeEnrollmentError(writer, http.StatusInternalServerError, "OFFER_UNAVAILABLE")
		return
	}
	writer.Header().Set("Content-Type", "application/json")
	writer.WriteHeader(http.StatusCreated)
	_ = json.NewEncoder(writer).Encode(nodes.EnrollmentOfferResponse{Offer: offer, URI: uri})
}

func (handler *nodeEnrollmentOperatorHandler) authenticate(request *http.Request) bool {
	if request == nil || handler == nil || handler.token == "" {
		return false
	}
	token, found := strings.CutPrefix(request.Header.Get("Authorization"), "Bearer ")
	return found && constantTimeStringEqual(token, handler.token)
}

func writeNodeEnrollmentError(writer http.ResponseWriter, status int, code string) {
	writer.Header().Set("Content-Type", "application/json")
	writer.WriteHeader(status)
	_ = json.NewEncoder(writer).Encode(map[string]string{"error": code})
}
