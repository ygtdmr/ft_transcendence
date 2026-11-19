package validate

import (
	"bytes"
	"encoding/json"
	"io"
	"mime"
	"net/http"
	"strings"
)

// DefaultMaxBytes is the request body limit when Options.MaxBytes is zero.
const DefaultMaxBytes = 64 << 10

// Options configures DecodeJSON.
type Options struct {
	// MaxBytes caps the body size. Default 64 KiB. Larger bodies are rejected
	// without being read to the end.
	MaxBytes int64
}

// DecodeJSON reads the request body as exactly one JSON object into a T, then
// calls T's Validate method, and returns the value only if both succeed. The
// type constraint makes validation impossible to forget: a DTO without a
// Validate method does not compile here (give it one that returns nil if it
// truly needs no checks).
//
// It rejects, with a *BodyError (400 VALIDATION_ERROR): a media type other than
// application/json (an optional charset must be utf-8), an empty body, a body
// over the limit, malformed JSON, a top-level value that is not an object,
// fields the DTO does not declare, fields repeated (also under different
// letter case, which encoding/json would silently merge), wrong value types and
// anything after the object. Validate's *FieldError (422) is returned as is.
//
//	type createRoom struct{ MaxPlayers int `json:"max_players"` }
//	func (r *createRoom) Validate() error { return validate.IntBetween("max_players", int64(r.MaxPlayers), 4, 20) }
//
//	req, err := validate.DecodeJSON[createRoom](w, r, validate.Options{})
func DecodeJSON[T any, PT interface {
	*T
	Validate() error
}](w http.ResponseWriter, r *http.Request, opts Options) (T, error) {
	var zero T

	mediaType, params, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || mediaType != "application/json" {
		return zero, &BodyError{}
	}
	for name, value := range params {
		if name != "charset" || !strings.EqualFold(value, "utf-8") {
			return zero, &BodyError{}
		}
	}

	limit := opts.MaxBytes
	if limit <= 0 {
		limit = DefaultMaxBytes
	}
	data, err := io.ReadAll(http.MaxBytesReader(w, r.Body, limit))
	if err != nil {
		return zero, &BodyError{}
	}
	data = bytes.TrimSpace(data)
	// json.Valid accepts exactly one JSON value, so anything after the object
	// (a second object, stray text) fails here and cannot reach the decoder.
	if len(data) == 0 || data[0] != '{' || !json.Valid(data) {
		return zero, &BodyError{}
	}
	if duplicate, err := hasDuplicateKeys(data); duplicate || err != nil {
		return zero, &BodyError{}
	}

	var dst T
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&dst); err != nil {
		return zero, &BodyError{}
	}

	if err := PT(&dst).Validate(); err != nil {
		return zero, err
	}
	return dst, nil
}

// hasDuplicateKeys reports whether any object in the document repeats a key,
// comparing case-insensitively because encoding/json matches struct fields that
// way. A repeated key means two parsers could read different values, which is
// how a gateway check and the service can disagree.
func hasDuplicateKeys(data []byte) (bool, error) {
	return scanValue(json.NewDecoder(bytes.NewReader(data)))
}

func scanValue(dec *json.Decoder) (bool, error) {
	token, err := dec.Token()
	if err != nil {
		return false, err
	}
	delim, isDelim := token.(json.Delim)
	if !isDelim {
		return false, nil
	}
	switch delim {
	case '{':
		seen := map[string]struct{}{}
		for dec.More() {
			keyToken, err := dec.Token()
			if err != nil {
				return false, err
			}
			key := strings.ToLower(keyToken.(string))
			if _, repeated := seen[key]; repeated {
				return true, nil
			}
			seen[key] = struct{}{}
			if duplicate, err := scanValue(dec); duplicate || err != nil {
				return duplicate, err
			}
		}
	case '[':
		for dec.More() {
			if duplicate, err := scanValue(dec); duplicate || err != nil {
				return duplicate, err
			}
		}
	}
	_, err = dec.Token() // the closing } or ]
	return false, err
}
