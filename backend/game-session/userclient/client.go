package userclient

import (
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"game-session/room"
	"game-session/service"
)

type Client struct {
	baseURL    string
	credential string
	httpClient *http.Client
}

func New(baseURL string, credential string) *Client {
	return &Client{
		baseURL:    baseURL,
		credential: credential,
		httpClient: &http.Client{Timeout: 5 * time.Second},
	}
}

type profileData struct {
	UserID    int64  `json:"user_id"`
	Username  string `json:"username"`
	AvatarURL string `json:"avatar_url"`
}

type profileResponse struct {
	Data profileData `json:"data"`
}

func (client *Client) buildProfileRequest(userID int64) (*http.Request, error) {
	url := fmt.Sprintf("%s/internal/v1/profiles/%d", client.baseURL, userID)
	request, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	request.Header.Set("Authorization", "Bearer "+client.credential)
	request.Header.Set("Accept", "application/json")
	return request, nil
}

func (client *Client) fetchProfile(userID int64) (room.Profile, bool, error) {
	request, err := client.buildProfileRequest(userID)
	if err != nil {
		return room.Profile{}, false, err
	}
	response, err := client.httpClient.Do(request)
	if err != nil {
		return room.Profile{}, false, fmt.Errorf("%w: %v", service.ErrProfileSourceUnavailable, err)
	}
	defer response.Body.Close()

	switch {
	case response.StatusCode == http.StatusOK:
		var body profileResponse
		if err := json.NewDecoder(response.Body).Decode(&body); err != nil {
			return room.Profile{}, false, fmt.Errorf("user-service profile %d: invalid response body: %w", userID, err)
		}
		return room.Profile{Username: body.Data.Username, AvatarURL: body.Data.AvatarURL}, true, nil
	case response.StatusCode == http.StatusNotFound, response.StatusCode == http.StatusConflict:
		return room.Profile{}, false, nil
	case response.StatusCode >= http.StatusInternalServerError:
		return room.Profile{}, false, fmt.Errorf("%w: status %d(internal)", service.ErrProfileSourceUnavailable, response.StatusCode)
	default:
		return room.Profile{}, false, fmt.Errorf("user-service profile %d: unexpected status %d", userID, response.StatusCode)
	}
}

func (client *Client) ProfilesByUserIDs(userIDs []int64) (map[int64]room.Profile, error) {
	profiles := make(map[int64]room.Profile, len(userIDs))
	for _, userID := range userIDs {
		profile, found, err := client.fetchProfile(userID)
		if err != nil {
			return nil, err
		}
		if found {
			profiles[userID] = profile
		}
	}
	return profiles, nil
}
