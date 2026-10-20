package api

import (
	jsonv1 "encoding/json"
	jsonv2 "encoding/json/v2"
)

func unmarshalJSON(data []byte, out any) error {
	return jsonv2.Unmarshal(data, out, jsonv1.DefaultOptionsV1())
}

func unmarshalString(data []byte, out *string) error {
	return jsonv2.Unmarshal(data, out, jsonv1.DefaultOptionsV1())
}

func unmarshalObject(data []byte, out any) error {
	return jsonv2.Unmarshal(data, out, jsonv1.DefaultOptionsV1())
}

func marshalJSON(in any) ([]byte, error) {
	return jsonv2.Marshal(in, jsonv1.DefaultOptionsV1())
}
