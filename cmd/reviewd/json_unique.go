package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
)

// Reject ambiguous object keys before typed decoding of trusted configuration.
func uniqueCommandJSON(b []byte) error {
	d := json.NewDecoder(bytes.NewReader(b))
	var value func() error
	value = func() error {
		t, e := d.Token()
		if e != nil {
			return e
		}
		delim, ok := t.(json.Delim)
		if !ok {
			return nil
		}
		switch delim {
		case '{':
			seen := map[string]bool{}
			for d.More() {
				token, e := d.Token()
				if e != nil {
					return e
				}
				key, ok := token.(string)
				if !ok || seen[key] {
					return errors.New("duplicate or invalid JSON object key")
				}
				seen[key] = true
				if e = value(); e != nil {
					return e
				}
			}
			end, e := d.Token()
			if e != nil {
				return e
			}
			if end != json.Delim('}') {
				return errors.New("invalid JSON object")
			}
		case '[':
			for d.More() {
				if e = value(); e != nil {
					return e
				}
			}
			end, e := d.Token()
			if e != nil {
				return e
			}
			if end != json.Delim(']') {
				return errors.New("invalid JSON array")
			}
		default:
			return errors.New("unexpected JSON delimiter")
		}
		return nil
	}
	if e := value(); e != nil {
		return e
	}
	if _, e := d.Token(); e != io.EOF {
		return errors.New("trailing JSON")
	}
	return nil
}
