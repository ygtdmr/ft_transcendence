package httpresponse

import (
	"encoding/json"
	"net/http"
	"time"
)

// ErrorCodeRecorder is implemented by response writers that want to learn the
// REST error code WriteError sends (access logs and metrics use it). Writers
// that do not implement it are unaffected.
type ErrorCodeRecorder interface {
	RecordErrorCode(code string)
}

// Time is a time.Time that marshals as an ISO-8601 / RFC 3339 UTC string such as
// "2026-11-05T17:04:05.123Z". Use it for every timestamp field in a response.
type Time time.Time

// MarshalJSON implements json.Marshaler.
func (t Time) MarshalJSON() ([]byte, error) {
	return json.Marshal(time.Time(t).UTC().Format(time.RFC3339Nano))
}

// UnmarshalJSON implements json.Unmarshaler.
func (t *Time) UnmarshalJSON(data []byte) error {
	var parsed time.Time
	if err := json.Unmarshal(data, &parsed); err != nil {
		return err
	}
	*t = Time(parsed.UTC())
	return nil
}

// WriteData writes {"data": data} with the given 2xx status. Use WriteNoContent
// for 204. A status outside 2xx (other than 204) or a value that cannot be
// encoded produces the generic internal error instead, so a handler bug never
// emits a malformed success body.
func WriteData(writer http.ResponseWriter, status int, data any) {
	if status < 200 || status > 299 || status == http.StatusNoContent {
		WriteError(writer, InternalError())
		return
	}
	body, err := json.Marshal(struct {
		Data any `json:"data"`
	}{Data: data})
	if err != nil {
		WriteError(writer, InternalError())
		return
	}
	setAPIHeaders(writer.Header())
	writer.Header().Set("Content-Type", "application/json; charset=utf-8")
	writer.WriteHeader(status)
	_, _ = writer.Write(append(body, '\n'))
}

// WriteNoContent writes 204 with no body and no Content-Type.
func WriteNoContent(writer http.ResponseWriter) {
	setAPIHeaders(writer.Header())
	writer.WriteHeader(http.StatusNoContent)
}
