package openapigo

import "encoding/json"

// JSONCodec encodes and decodes JSON. Override via WithJSONCodec for
// custom serialization (e.g., jsoniter, sonic).
type JSONCodec interface {
	Marshal(v any) ([]byte, error)
	Unmarshal(data []byte, v any) error
}

type defaultCodec struct{}

func (defaultCodec) Marshal(v any) ([]byte, error)      { return json.Marshal(v) }
func (defaultCodec) Unmarshal(data []byte, v any) error { return json.Unmarshal(data, v) }
