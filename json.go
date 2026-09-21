package jev

import (
	"encoding/json/jsontext"
	json "encoding/json/v2"
)

// encodeOptions is the wire format.
//
// encoding/json (v1) escapes '<', '>', and '&' so a prompt changes on the
// wire. encoding/json/v2 does not. EscapeForHTML(false) keeps that explicit,
// including if a caller later mixes in v1-compatible options.
// Deterministic sorts object keys, so the same request encodes to the same bytes.
var encodeOptions = []json.Options{
	json.Deterministic(true),
	jsontext.EscapeForHTML(false),
	jsontext.EscapeForJS(false),
}

// decodeOptions ignores members this version of the client does not know,
// so a new response field does not break older callers.
var decodeOptions = []json.Options{
	json.RejectUnknownMembers(false),
}

func marshal(v any) ([]byte, error) {
	return json.Marshal(v, encodeOptions...)
}

func unmarshal(b []byte, v any) error {
	return json.Unmarshal(b, v, decodeOptions...)
}
