package ipc_test

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"strings"
	"sync"
	"testing"

	"github.com/Iyed-M/offbeat/internal/ipc"
)

func TestProtocolVersionIsOne(t *testing.T) {
	if ipc.ProtocolVersion != 1 {
		t.Fatalf("ProtocolVersion=%d want 1", ipc.ProtocolVersion)
	}
}

func TestMaxMessageBytesIsPositive(t *testing.T) {
	if ipc.MaxMessageBytes <= 0 {
		t.Fatalf("MaxMessageBytes=%d want >0", ipc.MaxMessageBytes)
	}
}

func TestStableErrorCodes(t *testing.T) {
	want := []ipc.ErrorCode{
		ipc.CodeInvalidRequest,
		ipc.CodeUnsupportedVersion,
		ipc.CodeFailedPrecondition,
		ipc.CodeInternal,
	}
	seen := map[ipc.ErrorCode]bool{}
	for _, c := range want {
		if c == "" {
			t.Fatal("empty error code constant")
		}
		if seen[c] {
			t.Fatalf("duplicate error code %q", c)
		}
		seen[c] = true
	}
}

func TestEncodeDecodeRequestRoundTrip(t *testing.T) {
	req := ipc.Request{Version: ipc.ProtocolVersion, Command: "status"}
	data, err := ipc.Encode(req)
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}
	var got ipc.Request
	if err := ipc.Decode(data, &got); err != nil {
		t.Fatalf("Decode: %v", err)
	}
	if got.Version != req.Version || got.Command != req.Command {
		t.Fatalf("got %+v want %+v", got, req)
	}
}

func TestEncodeProducesNoNewline(t *testing.T) {
	req := ipc.Request{Version: 1, Command: "status"}
	data, err := ipc.Encode(req)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "\n") {
		t.Fatalf("Encode appended newline: %q", data)
	}
}

func TestDecodeRejectsUnknownFields(t *testing.T) {
	payload := []byte(`{"version":1,"command":"status","extra":"nope"}`)
	var req ipc.Request
	err := ipc.Decode(payload, &req)
	if err == nil {
		t.Fatal("expected error for unknown field, got nil")
	}
}

func TestDecodeRejectsMissingVersion(t *testing.T) {
	client, server := net.Pipe()
	t.Cleanup(func() { _ = client.Close() })

	go func() {
		_ = ipc.Serve(context.Background(), server, func(context.Context, ipc.Request) (any, error) {
			t.Errorf("handler must not be called for missing version")
			return nil, nil
		}, nil)
	}()

	if _, err := client.Write([]byte(`{"command":"status"}` + "\n")); err != nil {
		t.Fatalf("write: %v", err)
	}

	resp := mustReadResponse(t, client)
	if resp.Error == nil {
		t.Fatalf("expected error response, got result=%+v", resp.Result)
	}
	if resp.Error.Code != ipc.CodeInvalidRequest {
		t.Fatalf("got code=%q want %q", resp.Error.Code, ipc.CodeInvalidRequest)
	}
}

func TestDecodeRejectsTrailingJSONValue(t *testing.T) {
	client, server := net.Pipe()
	t.Cleanup(func() { _ = client.Close() })

	go func() {
		_ = ipc.Serve(context.Background(), server, func(context.Context, ipc.Request) (any, error) {
			t.Errorf("handler must not be called when extra JSON follows the request")
			return nil, nil
		}, nil)
	}()

	if _, err := client.Write([]byte(`{"version":1,"command":"status"} {}` + "\n")); err != nil {
		t.Fatalf("write: %v", err)
	}

	resp := mustReadResponse(t, client)
	if resp.Error == nil {
		t.Fatalf("expected error response, got result=%+v", resp.Result)
	}
	if resp.Error.Code != ipc.CodeInvalidRequest {
		t.Fatalf("got code=%q want %q", resp.Error.Code, ipc.CodeInvalidRequest)
	}
}

func TestDecodeRejectsMissingFields(t *testing.T) {
	client, server := net.Pipe()
	t.Cleanup(func() { _ = client.Close() })

	go func() {
		_ = ipc.Serve(context.Background(), server, func(context.Context, ipc.Request) (any, error) {
			t.Errorf("handler must not be called for missing command")
			return nil, nil
		}, nil)
	}()

	if _, err := client.Write([]byte(`{"version":1}` + "\n")); err != nil {
		t.Fatalf("write: %v", err)
	}

	resp := mustReadResponse(t, client)
	if resp.Error == nil {
		t.Fatalf("expected error response, got result=%+v", resp.Result)
	}
	if resp.Error.Code != ipc.CodeInvalidRequest {
		t.Fatalf("got code=%q want %q", resp.Error.Code, ipc.CodeInvalidRequest)
	}
}

func TestDecodeRejectsMalformedJSON(t *testing.T) {
	var req ipc.Request
	err := ipc.Decode([]byte(`{not json`), &req)
	if err == nil {
		t.Fatal("expected error for malformed JSON, got nil")
	}
}

func TestEncodeDecodeErrorRoundTrip(t *testing.T) {
	want := ipc.NewError(ipc.CodeInvalidRequest, "bad field")
	data, err := ipc.Encode(want)
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}
	var got ipc.Error
	if err := ipc.Decode(data, &got); err != nil {
		t.Fatalf("Decode: %v", err)
	}
	if got != want {
		t.Fatalf("got %+v want %+v", got, want)
	}
}

func TestEncodeRejectsUnsupportedType(t *testing.T) {
	if _, err := ipc.Encode(make(chan int)); err == nil {
		t.Fatal("expected error for unsupported type, got nil")
	}
}

func TestReadFrameReadsSingleLine(t *testing.T) {
	r := bufio.NewReader(strings.NewReader(`{"version":1,"command":"status"}` + "\ntrailing"))
	got, err := ipc.ReadFrame(r)
	if err != nil {
		t.Fatalf("ReadFrame: %v", err)
	}
	want := `{"version":1,"command":"status"}`
	if string(got) != want {
		t.Fatalf("got %q want %q", got, want)
	}
}

func TestReadFrameRejectsMessageLargerThanMax(t *testing.T) {
	big := strings.Repeat("a", ipc.MaxMessageBytes+1)
	r := bufio.NewReader(strings.NewReader(big + "\n"))
	_, err := ipc.ReadFrame(r)
	if err == nil {
		t.Fatal("expected error for message over MaxMessageBytes, got nil")
	}
	if !errors.Is(err, ipc.ErrMessageTooLarge) {
		t.Fatalf("err=%v want ErrMessageTooLarge", err)
	}
}

func TestReadFrameRejectsUnterminatedStream(t *testing.T) {
	r := bufio.NewReader(strings.NewReader(`{"version":1,"command":"status"}`))
	_, err := ipc.ReadFrame(r)
	if err == nil || !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Fatalf("err=%v want io.ErrUnexpectedEOF", err)
	}
}

func TestWriteFrameAppendsNewline(t *testing.T) {
	var buf strings.Builder
	if err := ipc.WriteFrame(&buf, []byte(`{"x":1}`)); err != nil {
		t.Fatalf("WriteFrame: %v", err)
	}
	if got := buf.String(); got != `{"x":1}`+"\n" {
		t.Fatalf("got %q", got)
	}
}

func TestFrameBoundaryIsConsistentAcrossReadAndWrite(t *testing.T) {
	payload := make([]byte, ipc.MaxMessageBytes)
	for i := range payload {
		payload[i] = 'a'
	}

	var buf strings.Builder
	if err := ipc.WriteFrame(&buf, payload); err != nil {
		t.Fatalf("WriteFrame at boundary: %v", err)
	}
	if _, err := ipc.ReadFrame(bufio.NewReader(strings.NewReader(buf.String()))); err != nil {
		t.Fatalf("ReadFrame at boundary: %v", err)
	}

	oversize := make([]byte, ipc.MaxMessageBytes+1)
	if err := ipc.WriteFrame(&buf, oversize); !errors.Is(err, ipc.ErrMessageTooLarge) {
		t.Fatalf("WriteFrame over limit err=%v want ErrMessageTooLarge", err)
	}
}

func TestWriteFrameRejectsOversize(t *testing.T) {
	var buf strings.Builder
	big := make([]byte, ipc.MaxMessageBytes+1)
	if err := ipc.WriteFrame(&buf, big); err == nil {
		t.Fatal("expected error for oversized frame")
	}
}

func TestServeReturnsErrorResponseForUnsupportedVersion(t *testing.T) {
	client, server := net.Pipe()
	t.Cleanup(func() { _ = client.Close() })

	go func() {
		_ = ipc.Serve(context.Background(), server, func(context.Context, ipc.Request) (any, error) {
			t.Errorf("handler must not be called for unsupported version")
			return nil, nil
		}, nil)
	}()

	if _, err := client.Write([]byte(`{"version":99,"command":"status"}` + "\n")); err != nil {
		t.Fatalf("write: %v", err)
	}

	resp := mustReadResponse(t, client)
	if resp.Error == nil {
		t.Fatalf("expected error response, got result=%+v", resp.Result)
	}
	if resp.Error.Code != ipc.CodeUnsupportedVersion {
		t.Fatalf("got code=%q want %q", resp.Error.Code, ipc.CodeUnsupportedVersion)
	}
	if resp.Version != ipc.ProtocolVersion {
		t.Fatalf("got version=%d want %d", resp.Version, ipc.ProtocolVersion)
	}
}

func TestServeReturnsErrorResponseForMalformedJSON(t *testing.T) {
	client, server := net.Pipe()
	t.Cleanup(func() { _ = client.Close() })

	go func() {
		_ = ipc.Serve(context.Background(), server, func(context.Context, ipc.Request) (any, error) {
			t.Errorf("handler must not be called for malformed payload")
			return nil, nil
		}, nil)
	}()

	if _, err := client.Write([]byte("{not json" + "\n")); err != nil {
		t.Fatalf("write: %v", err)
	}

	resp := mustReadResponse(t, client)
	if resp.Error == nil {
		t.Fatalf("expected error response, got result=%+v", resp.Result)
	}
	if resp.Error.Code != ipc.CodeInvalidRequest {
		t.Fatalf("got code=%q want %q", resp.Error.Code, ipc.CodeInvalidRequest)
	}
}

func TestServeReturnsErrorResponseForUnknownCommand(t *testing.T) {
	client, server := net.Pipe()
	t.Cleanup(func() { _ = client.Close() })

	go func() {
		_ = ipc.Serve(context.Background(), server, func(_ context.Context, req ipc.Request) (any, error) {
			if req.Command != "status" {
				return nil, ipc.NewError(ipc.CodeInvalidRequest, "unknown command: "+req.Command)
			}
			return map[string]any{"ok": true}, nil
		}, nil)
	}()

	if _, err := client.Write([]byte(`{"version":1,"command":"nope"}` + "\n")); err != nil {
		t.Fatalf("write: %v", err)
	}

	resp := mustReadResponse(t, client)
	if resp.Error == nil {
		t.Fatalf("expected error response, got result=%+v", resp.Result)
	}
	if resp.Error.Code != ipc.CodeInvalidRequest {
		t.Fatalf("got code=%q want %q", resp.Error.Code, ipc.CodeInvalidRequest)
	}
}

func TestServeReturnsResultForValidStatus(t *testing.T) {
	client, server := net.Pipe()
	t.Cleanup(func() { _ = client.Close() })

	want := map[string]any{"daemon_version": "0.0.0-m1", "pid": 4242}
	var observed ipc.Request
	var mu sync.Mutex

	go func() {
		_ = ipc.Serve(context.Background(), server, func(_ context.Context, req ipc.Request) (any, error) {
			mu.Lock()
			observed = req
			mu.Unlock()
			return want, nil
		}, nil)
	}()

	if _, err := client.Write([]byte(`{"version":1,"command":"status"}` + "\n")); err != nil {
		t.Fatalf("write: %v", err)
	}

	resp := mustReadResponse(t, client)
	if resp.Error != nil {
		t.Fatalf("got error %+v", resp.Error)
	}
	mu.Lock()
	got := observed
	mu.Unlock()
	if got.Command != "status" {
		t.Fatalf("handler saw %+v", got)
	}

	// Decode result into map for stable comparison.
	raw, err := json.Marshal(resp.Result)
	if err != nil {
		t.Fatalf("re-marshal result: %v", err)
	}
	var gotResult map[string]any
	if err := json.Unmarshal(raw, &gotResult); err != nil {
		t.Fatalf("unmarshal result: %v", err)
	}
	if gotResult["daemon_version"] != want["daemon_version"] {
		t.Fatalf("daemon_version=%v want %v", gotResult["daemon_version"], want["daemon_version"])
	}
	if gotResult["pid"] != float64(4242) {
		t.Fatalf("pid=%v want 4242", gotResult["pid"])
	}
}

func TestServeClosesConnAfterOneRequest(t *testing.T) {
	client, server := net.Pipe()
	t.Cleanup(func() { _ = client.Close() })

	finished := make(chan struct{})
	go func() {
		defer close(finished)
		_ = ipc.Serve(context.Background(), server, func(context.Context, ipc.Request) (any, error) {
			return map[string]any{"ok": true}, nil
		}, nil)
	}()

	if _, err := client.Write([]byte(`{"version":1,"command":"status"}` + "\n")); err != nil {
		t.Fatalf("write: %v", err)
	}
	mustReadResponse(t, client)

	select {
	case <-finished:
	case <-timeAfter():
		t.Fatal("Serve did not return after one request")
	}
}

func mustReadResponse(t *testing.T, r io.Reader) ipc.Response {
	t.Helper()
	data, err := bufio.NewReader(r).ReadBytes('\n')
	if err != nil {
		t.Fatalf("read response: %v", err)
	}
	var resp ipc.Response
	if err := ipc.Decode(data, &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	return resp
}
