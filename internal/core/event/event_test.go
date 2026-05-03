package event

import (
	"encoding/json"
	"errors"
	"testing"
)

type samplePayload struct {
	Hello string `json:"hello"`
}

func decodeSample(_ uint32, payload []byte) (any, error) {
	var s samplePayload
	if err := json.Unmarshal(payload, &s); err != nil {
		return nil, err
	}
	return s, nil
}

func TestEnvelopeJSONRoundTrip(t *testing.T) {
	t.Parallel()

	original := Envelope{
		Type:    "sample",
		Version: 1,
		Payload: json.RawMessage(`{"hello":"world"}`),
	}

	encoded, err := json.Marshal(original)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var got Envelope
	if err := json.Unmarshal(encoded, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if got.Type != original.Type || got.Version != original.Version {
		t.Fatalf("envelope mismatch: got %+v want %+v", got, original)
	}
	if string(got.Payload) != string(original.Payload) {
		t.Fatalf("payload mismatch: got %s want %s", got.Payload, original.Payload)
	}
}

func TestRegistryDecodeKnownType(t *testing.T) {
	t.Parallel()

	r := NewRegistry()
	r.Register("sample", 1, decodeSample)

	env := Envelope{Type: "sample", Version: 1, Payload: json.RawMessage(`{"hello":"world"}`)}
	got, err := r.Decode(env)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	s, ok := got.(samplePayload)
	if !ok {
		t.Fatalf("got %T want samplePayload", got)
	}
	if s.Hello != "world" {
		t.Fatalf("payload mismatch: got %+v", s)
	}
}

func TestRegistryDecodeSkipsUnknownType(t *testing.T) {
	t.Parallel()

	r := NewRegistry()
	r.Register("sample", 1, decodeSample)

	env := Envelope{Type: "future-type", Version: 1, Payload: json.RawMessage(`{}`)}
	got, err := r.Decode(env)
	if !errors.Is(err, ErrSkipUnknownType) {
		t.Fatalf("err: got %v want ErrSkipUnknownType", err)
	}
	if got != nil {
		t.Fatalf("value: got %v want nil for skip", got)
	}
}

func TestRegistryDecodeRejectsUnsupportedVersion(t *testing.T) {
	t.Parallel()

	r := NewRegistry()
	r.Register("sample", 1, decodeSample)

	env := Envelope{Type: "sample", Version: 2, Payload: json.RawMessage(`{"hello":"world"}`)}
	_, err := r.Decode(env)
	if !errors.Is(err, ErrUnsupportedVersion) {
		t.Fatalf("err: got %v want ErrUnsupportedVersion", err)
	}
}

func TestRegistryDecoderUpgradesOlderVersion(t *testing.T) {
	t.Parallel()

	r := NewRegistry()
	r.Register("sample", 2, func(version uint32, payload []byte) (any, error) {
		var s samplePayload
		if err := json.Unmarshal(payload, &s); err != nil {
			return nil, err
		}
		if version == 1 {
			s.Hello = "v1:" + s.Hello
		}
		return s, nil
	})

	env := Envelope{Type: "sample", Version: 1, Payload: json.RawMessage(`{"hello":"world"}`)}
	got, err := r.Decode(env)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	s := got.(samplePayload)
	if s.Hello != "v1:world" {
		t.Fatalf("decoder did not see version: got %+v", s)
	}
}
