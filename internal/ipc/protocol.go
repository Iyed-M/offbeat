// Package ipc implements the versioned JSON Lines control protocol used
// between the offbeat CLI and the offbeatd daemon over a private Unix
// domain socket.
//
// A single connection carries exactly one request and one response, both
// newline-delimited. The wire format is strict: unknown fields, missing
// required fields, and malformed JSON all map to invalid_request. Frames
// larger than MaxMessageBytes are rejected before they can be decoded.
package ipc

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"time"
)

const (
	// ProtocolVersion is the version carried in every request and response.
	ProtocolVersion = 1
	// MaxMessageBytes is the largest single frame (request or response)
	// the protocol will read or write on the wire.
	MaxMessageBytes = 1 << 20 // 1 MiB
	// FrameNewline marks the end of a single JSON Lines frame.
	FrameNewline = '\n'
)

// ErrorCode is the agreed structured code attached to a daemon-generated
// error response. New codes require an ADR.
type ErrorCode string

const (
	// CodeInvalidRequest is returned when the request payload cannot be
	// understood: malformed JSON, missing fields, unknown fields, or an
	// unsupported command.
	CodeInvalidRequest ErrorCode = "invalid_request"
	// CodeUnsupportedVersion is returned when the request version is not
	// the one this daemon speaks.
	CodeUnsupportedVersion ErrorCode = "unsupported_version"
	// CodeFailedPrecondition is returned when the request was understood
	// but the daemon cannot fulfil it because of local state.
	CodeFailedPrecondition ErrorCode = "failed_precondition"
	// CodeInternal is returned for unexpected server-side failures.
	CodeInternal ErrorCode = "internal"
)

// Request is the versioned command envelope sent by a CLI over one
// connection.
type Request struct {
	// versionSet records that strict JSON decoding validated the required
	// version field. It lets the dispatcher distinguish an invalid request
	// from an explicitly unsupported version.
	versionSet         bool
	Version            int                      `json:"version"`
	Command            string                   `json:"command"`
	Missing            *MissingRequest          `json:"missing,omitempty"`
	Acquire            *AcquireRequest          `json:"acquire,omitempty"`
	AcquisitionInspect *AcquisitionTrackRequest `json:"acquisition_inspect,omitempty"`
	AcquisitionStatus  *AcquisitionIDRequest    `json:"acquisition_status,omitempty"`
	AcquisitionList    *AcquisitionListRequest  `json:"acquisition_list,omitempty"`
	AcquisitionRetry   *AcquisitionIDRequest    `json:"acquisition_retry,omitempty"`
}

// UnmarshalJSON strictly validates the request envelope before making it
// available to the dispatcher. json.Decoder's DisallowUnknownFields does not
// apply within a custom unmarshaller, so unknown fields are rejected here.
func (r *Request) UnmarshalJSON(data []byte) error {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		return err
	}
	for name := range fields {
		if name != "version" && name != "command" && name != "missing" && name != "acquire" && name != "acquisition_inspect" && name != "acquisition_status" && name != "acquisition_list" && name != "acquisition_retry" {
			return fmt.Errorf("unknown request field %q", name)
		}
	}

	version, ok := fields["version"]
	if !ok || isJSONNull(version) {
		return errors.New("missing required field \"version\"")
	}
	var parsedVersion int
	if err := json.Unmarshal(version, &parsedVersion); err != nil {
		return fmt.Errorf("decode version: %w", err)
	}

	command, ok := fields["command"]
	if !ok || isJSONNull(command) {
		return errors.New("missing required field \"command\"")
	}
	var parsedCommand string
	if err := json.Unmarshal(command, &parsedCommand); err != nil {
		return fmt.Errorf("decode command: %w", err)
	}
	var missing *MissingRequest
	if raw, ok := fields["missing"]; ok {
		if parsedCommand != "missing" || isJSONNull(raw) {
			return errors.New("missing arguments are only valid for missing requests")
		}
		var page struct {
			AfterURI      string `json:"after_uri"`
			StateRevision *int64 `json:"state_revision"`
		}
		if err := Decode(raw, &page); err != nil {
			return err
		}
		if page.AfterURI == "" || page.StateRevision == nil || *page.StateRevision < 0 {
			return errors.New("missing continuation requires after_uri and non-negative state_revision")
		}
		missing = &MissingRequest{AfterURI: page.AfterURI, StateRevision: *page.StateRevision}
	}
	var acquire *AcquireRequest
	if raw, ok := fields["acquire"]; ok {
		if parsedCommand != "acquire" || isJSONNull(raw) {
			return errors.New("acquire arguments are only valid for acquire requests")
		}
		var parsed AcquireRequest
		if err := Decode(raw, &parsed); err != nil {
			return err
		}
		if err := ValidateAcquisitionTrackURI(parsed.TrackURI); err != nil {
			return err
		}
		if err := ValidateAcquisitionSource(parsed.SourceURL); err != nil {
			return err
		}
		acquire = &parsed
	}
	var acquisitionInspect *AcquisitionTrackRequest
	if raw, ok := fields["acquisition_inspect"]; ok {
		if parsedCommand != "acquire.inspect" || isJSONNull(raw) {
			return errors.New("acquisition_inspect arguments are only valid for acquire.inspect requests")
		}
		var parsed AcquisitionTrackRequest
		if err := Decode(raw, &parsed); err != nil {
			return err
		}
		if err := ValidateAcquisitionTrackURI(parsed.TrackURI); err != nil {
			return err
		}
		acquisitionInspect = &parsed
	}
	parseAcquisitionID := func(field, command string) (*AcquisitionIDRequest, error) {
		raw, ok := fields[field]
		if !ok {
			return nil, nil
		}
		if parsedCommand != command || isJSONNull(raw) {
			return nil, fmt.Errorf("%s arguments are only valid for %s requests", field, command)
		}
		var parsed AcquisitionIDRequest
		if err := Decode(raw, &parsed); err != nil {
			return nil, err
		}
		if parsed.ID <= 0 {
			return nil, errors.New("acquisition_id must be a positive integer")
		}
		return &parsed, nil
	}
	acquisitionStatus, err := parseAcquisitionID("acquisition_status", "acquire.status")
	if err != nil {
		return err
	}
	var acquisitionList *AcquisitionListRequest
	if raw, ok := fields["acquisition_list"]; ok {
		if parsedCommand != "acquire.status" || isJSONNull(raw) || acquisitionStatus != nil {
			return errors.New("acquisition_list arguments are only valid for aggregate acquire.status requests")
		}
		var parsed AcquisitionListRequest
		if err := Decode(raw, &parsed); err != nil {
			return err
		}
		if parsed.AfterID <= 0 {
			return errors.New("after_id must be a positive integer")
		}
		acquisitionList = &parsed
	}
	acquisitionRetry, err := parseAcquisitionID("acquisition_retry", "acquire.retry")
	if err != nil {
		return err
	}
	if parsedCommand == "acquire" && acquire == nil {
		return errors.New("acquire request requires acquire arguments")
	}
	if parsedCommand == "acquire.inspect" && acquisitionInspect == nil {
		return errors.New("acquire.inspect request requires acquisition_inspect arguments")
	}
	if parsedCommand == "acquire.retry" && acquisitionRetry == nil {
		return errors.New("acquire.retry request requires acquisition_retry arguments")
	}

	r.versionSet = true
	r.Version = parsedVersion
	r.Command = parsedCommand
	r.Missing = missing
	r.Acquire = acquire
	r.AcquisitionInspect = acquisitionInspect
	r.AcquisitionStatus = acquisitionStatus
	r.AcquisitionList = acquisitionList
	r.AcquisitionRetry = acquisitionRetry
	return nil
}

func isJSONNull(data []byte) bool {
	return bytes.Equal(bytes.TrimSpace(data), []byte("null"))
}

// Response is the versioned envelope sent back by the daemon over the same
// connection. Exactly one of Result or Error is populated.
type Response struct {
	Version int    `json:"version"`
	Result  any    `json:"result,omitempty"`
	Error   *Error `json:"error,omitempty"`
}

// Error is the structured error attached to a Response. It also
// implements the error interface so a Handler can return it directly.
type Error struct {
	Code    ErrorCode `json:"code"`
	Message string    `json:"message"`
}

// Error implements the error interface.
func (e Error) Error() string { return string(e.Code) + ": " + e.Message }

// NewError builds a structured error value for use as a Response.Error.
func NewError(code ErrorCode, msg string) Error {
	return Error{Code: code, Message: msg}
}

// ErrMessageTooLarge is returned when a frame exceeds MaxMessageBytes
// before a newline is seen.
var ErrMessageTooLarge = errors.New("ipc: message exceeds maximum size")

// Encode serialises v as a single JSON message. It returns an error if v
// cannot be JSON-encoded (for example a channel or function value).
func Encode(v any) ([]byte, error) {
	data, err := json.Marshal(v)
	if err != nil {
		return nil, fmt.Errorf("ipc encode: %w", err)
	}
	return data, nil
}

// Decode parses a single JSON message into v. Unknown fields are rejected
// so the protocol can evolve deliberately via ADRs. The byte slice must
// contain exactly one JSON value; trailing non-whitespace data is rejected.
func Decode(data []byte, v any) error {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return fmt.Errorf("ipc decode: %w", err)
	}
	if _, err := dec.Token(); err == nil {
		return fmt.Errorf("ipc decode: %w", errTrailingData)
	} else if !errors.Is(err, io.EOF) {
		return fmt.Errorf("ipc decode: %w", err)
	}
	return nil
}

var errTrailingData = errors.New("ipc: trailing data after JSON value")

// ReadFrame reads exactly one '\n'-terminated JSON message from r. The
// returned slice does not include the trailing newline. It returns
// ErrMessageTooLarge if the payload (excluding the newline) exceeds
// MaxMessageBytes, and io.ErrUnexpectedEOF if the underlying reader hits
// EOF before a newline.
func ReadFrame(r *bufio.Reader) ([]byte, error) {
	var buf []byte
	for {
		line, err := r.ReadSlice(FrameNewline)
		if len(line) > 0 {
			if len(buf)+len(line) > MaxMessageBytes+1 {
				return nil, ErrMessageTooLarge
			}
			buf = append(buf, line...)
		}
		if errors.Is(err, bufio.ErrBufferFull) {
			continue
		}
		if err != nil {
			if errors.Is(err, io.EOF) {
				return nil, fmt.Errorf("%w: %w", io.ErrUnexpectedEOF, io.EOF)
			}
			return nil, err
		}
		// The last byte is the FrameNewline; cap the payload (everything
		// before it) and reject if it is strictly larger than MaxMessageBytes.
		payload := buf[:len(buf)-1]
		if len(payload) > MaxMessageBytes {
			return nil, ErrMessageTooLarge
		}
		return payload, nil
	}
}

// WriteFrame writes msg followed by '\n' to w. It refuses to write a
// payload larger than MaxMessageBytes; the newline is never counted
// toward the limit, mirroring ReadFrame.
func WriteFrame(w io.Writer, msg []byte) error {
	if len(msg) > MaxMessageBytes {
		return ErrMessageTooLarge
	}
	if _, err := w.Write(msg); err != nil {
		return err
	}
	if _, err := w.Write([]byte{FrameNewline}); err != nil {
		return err
	}
	return nil
}

// Handler processes a single decoded request and returns either a result
// or a structured error. A Handler MUST NOT return both.
type Handler func(ctx context.Context, req Request) (result any, err error)

// Serve reads one request frame from conn, dispatches it to h, writes one
// response frame, and closes conn. It is intended to be called once per
// accepted connection. The returned error is the underlying I/O error if
// the connection could not be read or written; protocol-level errors are
// delivered to the client as a structured Response.Error.
//
// If logger is non-nil, each request is logged at info level and each
// dispatched command at debug level.
func Serve(ctx context.Context, conn net.Conn, h Handler, logger *slog.Logger) error {
	defer func() { _ = conn.Close() }()

	addr := conn.RemoteAddr()
	if logger != nil {
		logger.Info("control request received", "remote", addr)
	}

	if err := conn.SetReadDeadline(time.Now().Add(5 * time.Second)); err != nil {
		return fmt.Errorf("set read deadline: %w", err)
	}

	reader := bufio.NewReader(conn)
	frame, err := ReadFrame(reader)
	if err != nil {
		return writeProtocolError(conn, logger, addr, "read_request", err)
	}

	var req Request
	if err := Decode(frame, &req); err != nil {
		return writeProtocolError(conn, logger, addr, "decode_request", err)
	}
	if !req.versionSet || req.Command == "" {
		resp := Response{Version: ProtocolVersion, Error: ptr(NewError(CodeInvalidRequest, "missing required field"))}
		return writeResponse(conn, logger, addr, resp)
	}
	if req.Version != ProtocolVersion {
		resp := Response{Version: ProtocolVersion, Error: ptr(NewError(CodeUnsupportedVersion,
			fmt.Sprintf("unsupported protocol version %d", req.Version)))}
		return writeResponse(conn, logger, addr, resp)
	}

	if logger != nil {
		logger.Debug("control request dispatched", "remote", addr, "command", req.Command)
	}

	if err := conn.SetReadDeadline(time.Time{}); err != nil {
		return fmt.Errorf("clear read deadline: %w", err)
	}
	handlerCtx, cancelHandler := context.WithCancel(ctx)
	defer cancelHandler()
	go func() {
		var probe [1]byte
		if _, err := conn.Read(probe[:]); err != nil {
			cancelHandler()
		}
	}()
	result, herr := h(handlerCtx, req)
	cancelHandler()
	if herr != nil {
		var ipcErr Error
		if errors.As(herr, &ipcErr) {
			resp := Response{Version: ProtocolVersion, Error: ptr(ipcErr)}
			return writeResponse(conn, logger, addr, resp)
		}
		resp := Response{Version: ProtocolVersion, Error: ptr(NewError(CodeInternal, herr.Error()))}
		return writeResponse(conn, logger, addr, resp)
	}

	resp := Response{Version: ProtocolVersion, Result: result}
	return writeResponse(conn, logger, addr, resp)
}

// writeProtocolError writes a structured invalid_request response for any
// error encountered while reading or decoding the request frame.
func writeProtocolError(conn net.Conn, logger *slog.Logger, addr net.Addr, stage string, cause error) error {
	if logger != nil {
		logger.Debug("control request rejected", "remote", addr, "stage", stage, "err", cause)
	}
	resp := Response{Version: ProtocolVersion, Error: ptr(NewError(CodeInvalidRequest,
		fmt.Sprintf("could not parse request: %s", cause)))}
	return writeResponse(conn, logger, addr, resp)
}

func writeResponse(conn net.Conn, logger *slog.Logger, addr net.Addr, resp Response) error {
	if err := conn.SetWriteDeadline(time.Now().Add(5 * time.Second)); err != nil {
		return fmt.Errorf("set write deadline: %w", err)
	}
	data, err := Encode(resp)
	if err != nil {
		return fmt.Errorf("encode response: %w", err)
	}
	if err := WriteFrame(conn, data); err != nil {
		return fmt.Errorf("write response: %w", err)
	}
	if logger != nil && resp.Error != nil {
		logger.Info("control response sent", "remote", addr, "code", resp.Error.Code)
	} else if logger != nil {
		logger.Info("control response sent", "remote", addr)
	}
	return nil
}

func ptr[T any](v T) *T { return &v }
