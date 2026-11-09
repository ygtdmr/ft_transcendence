package requestid

import (
	"crypto/rand"
	"net/http"
)

const maxIDLength = 128

// selectID preserves one valid header value or returns a new random ID.
func selectID(header http.Header) string {
	values := header.Values(Header)
	if len(values) == 1 && validID(values[0]) {
		return values[0]
	}

	return rand.Text()
}

func validID(id string) bool {
	if len(id) == 0 || len(id) > maxIDLength {
		return false
	}

	for i := 0; i < len(id); i++ {
		char := id[i]
		if (char >= 'a' && char <= 'z') || (char >= 'A' && char <= 'Z') ||
			(char >= '0' && char <= '9') || char == '-' || char == '_' || char == '.' {
			continue
		}

		return false
	}

	return true
}
