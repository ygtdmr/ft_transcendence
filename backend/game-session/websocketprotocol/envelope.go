package websocketprotocol

import (
	"bytes"
	"encoding/json"

	"game-session/roomaction"
)

type ClientMessage struct {
	Type      string          `json:"type"`
	RequestID *string         `json:"request_id"`
	Payload   json.RawMessage `json:"payload"`
}

type AckReply struct {
	Type      string `json:"type"`
	RequestID string `json:"request_id"`
	OK        bool   `json:"ok"`
	Payload   any    `json:"payload"`
}

type ErrorReply struct {
	Type      string      `json:"type"`
	RequestID *string     `json:"request_id"`
	Error     ErrorDetail `json:"error"`
}

type ErrorDetail struct {
	Code    string         `json:"code"`
	Message string         `json:"message"`
	Details map[string]any `json:"details"`
}

type CommandOutcome struct {
	Ack      *AckReply
	Error    *ErrorReply
	Closures []roomaction.SocketClosure
}

func validRequestID(requestID string) bool {
	if len(requestID) < 1 || len(requestID) > 64 {
		return false
	}
	for i := 0; i < len(requestID); i++ {
		if requestID[i] < '!' || requestID[i] > '~' {
			return false
		}
	}
	return true
}

func parseEnvelope(rawMessage []byte) (ClientMessage, *ErrorReply) {
	var message ClientMessage
	if err := json.Unmarshal(rawMessage, &message); err != nil {
		return ClientMessage{}, newError(nil, errorInvalidEnvelope, nil)
	}

	if message.RequestID == nil {
		return ClientMessage{}, newError(nil, errorRequestIDRequired, map[string]any{"field": "request_id"})
	}
	if !validRequestID(*message.RequestID) {
		return ClientMessage{}, newError(message.RequestID, errorRequestIDInvalid, map[string]any{"field": "request_id"})
	}
	if message.Type == "" {
		return ClientMessage{}, newError(message.RequestID, errorTypeRequired, map[string]any{"field": "type"})
	}
	trimmed := bytes.TrimSpace(message.Payload)
	if len(trimmed) == 0 || trimmed[0] != '{' {
		return ClientMessage{}, newError(message.RequestID, errorPayloadRequired, map[string]any{"field": "payload"})
	}
	return message, nil
}
