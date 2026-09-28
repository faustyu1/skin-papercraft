package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"
	"time"
)

var (
	nickPattern     = regexp.MustCompile(`^[A-Za-z0-9_]{3,16}$`)
	errNoSkin       = errors.New("player or skin not found")
	mojangClient    = &http.Client{Timeout: 10 * time.Second}
	maxSkinDownload = int64(1 << 20)
)

// FetchSkin downloads a Java Edition player's current skin by nickname.
func FetchSkin(ctx context.Context, nick string) (skin []byte, name string, slim bool, err error) {
	var profile struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	}
	if err = getJSON(ctx, "https://api.mojang.com/users/profiles/minecraft/"+nick, &profile); err != nil {
		return nil, "", false, err
	}

	var session struct {
		Properties []struct {
			Name  string `json:"name"`
			Value string `json:"value"`
		} `json:"properties"`
	}
	if err = getJSON(ctx, "https://sessionserver.mojang.com/session/minecraft/profile/"+profile.ID, &session); err != nil {
		return nil, "", false, err
	}

	var textures struct {
		Textures struct {
			Skin struct {
				URL      string `json:"url"`
				Metadata struct {
					Model string `json:"model"`
				} `json:"metadata"`
			} `json:"SKIN"`
		} `json:"textures"`
	}
	for _, p := range session.Properties {
		if p.Name != "textures" {
			continue
		}
		raw, err := base64.StdEncoding.DecodeString(p.Value)
		if err != nil {
			return nil, "", false, err
		}
		if err := json.Unmarshal(raw, &textures); err != nil {
			return nil, "", false, err
		}
	}
	url := strings.Replace(textures.Textures.Skin.URL, "http://", "https://", 1)
	if url == "" {
		return nil, "", false, errNoSkin
	}

	body, err := get(ctx, url)
	if err != nil {
		return nil, "", false, err
	}
	defer body.Close()
	skin, err = io.ReadAll(io.LimitReader(body, maxSkinDownload))
	return skin, profile.Name, textures.Textures.Skin.Metadata.Model == "slim", err
}

func get(ctx context.Context, url string) (io.ReadCloser, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := mojangClient.Do(req)
	if err != nil {
		return nil, err
	}
	switch {
	case resp.StatusCode == http.StatusNoContent || resp.StatusCode == http.StatusNotFound:
		resp.Body.Close()
		return nil, errNoSkin
	case resp.StatusCode != http.StatusOK:
		resp.Body.Close()
		return nil, fmt.Errorf("GET %s: %s", url, resp.Status)
	}
	return resp.Body, nil
}

func getJSON(ctx context.Context, url string, v any) error {
	body, err := get(ctx, url)
	if err != nil {
		return err
	}
	defer body.Close()
	return json.NewDecoder(io.LimitReader(body, maxSkinDownload)).Decode(v)
}
