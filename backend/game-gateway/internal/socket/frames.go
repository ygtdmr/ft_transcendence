package socket

// Close codes this gateway uses. 4401, 4403 and 4404 are the contract's room
// socket codes (collections/v2/game-websocket, Errors); the others are RFC 6455.
const (
	CloseNormal          = 1000
	CloseGoingAway       = 1001
	CloseUnsupportedData = 1003
	CloseSessionRevoked  = 4401
)

// UnauthorizedFrame precedes the 4401 close of a revoked session: the room
// socket's error envelope with no request to correlate (error catalog,
// UNAUTHORIZED: "session becomes invalid after connection").
var UnauthorizedFrame = []byte(`{"type":"error","request_id":null,"error":{"code":"UNAUTHORIZED",` +
	`"message":"The session is no longer valid.","details":{}}}`)
