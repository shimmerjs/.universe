// Package oryx fetches a layout's layers and key legends from ZSA's public
// Oryx GraphQL endpoint, with a disk cache so renders keep working offline.
// The endpoint is unofficial and unversioned; the query pins an exact
// selection (combos is an object type -- a bare `combos` selection is a
// hard error).
package oryx

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
)

// DefaultEndpoint is the public Oryx GraphQL endpoint (no auth).
const DefaultEndpoint = "https://oryx.zsa.io/graphql"

// FirmwareURL is where Oryx serves a revision's compiled firmware binary --
// the exact bytes the revision's md5 field hashes (verified 2026-07-21).
// Same unofficial, unversioned surface as the GraphQL endpoint.
func FirmwareURL(hashID, revisionID string) string {
	return fmt.Sprintf("https://oryx.zsa.io/%s/%s/binary", hashID, revisionID)
}

// Geometry is pinned: this is the Moonlander foundation. Revisions are
// caller-chosen -- the flash loop fetches the exact hash the board's serial
// reports, and RevisionLatest tracks the live editor state (the response
// names the concrete revision it resolved to).
const geometry = "moonlander"

// RevisionLatest asks Oryx for the newest revision of a layout.
const RevisionLatest = "latest"

// Full layouts run ~100KB; the cap guards a misbehaving server.
const maxResponseBytes = 4 << 20

const getLayoutQuery = `query getLayout($hashId: String!, $geometry: String!, $revisionId: String!) {
  layout(hashId: $hashId, geometry: $geometry, revisionId: $revisionId) {
    hashId title geometry
    revision {
      hashId qmkVersion title createdAt model md5
      layers { title position keys }
      combos { keyIndices layerIdx trigger }
      config
    }
  }
}`

// Layout is the decoded getLayout payload, revision flattened. HashID is the
// layout's Oryx slug (the one in configure.zsa.io URLs); RevisionID names the
// specific revision "latest" resolved to.
type Layout struct {
	HashID     string          `json:"hashId"`
	Title      string          `json:"title"`
	Geometry   string          `json:"geometry"`
	RevisionID string          `json:"revisionId"`
	QmkVersion string          `json:"qmkVersion"`
	CreatedAt  string          `json:"createdAt"`
	Model      string          `json:"model"`
	MD5        string          `json:"md5"`
	Layers     []Layer         `json:"layers"`
	Combos     []Combo         `json:"combos,omitempty"`
	Config     json.RawMessage `json:"config,omitempty"`
}

// Layer is one Oryx layer. Keys are positional: no x/y on the wire, they zip
// index-for-index against the geometry's LAYOUT-macro key order.
type Layer struct {
	Title    string `json:"title"`
	Position int    `json:"position"`
	Keys     []Key  `json:"keys"`
}

// Key carries the legends for one physical key on one layer.
type Key struct {
	Tap         *Action `json:"tap,omitempty"`
	Hold        *Action `json:"hold,omitempty"`
	DoubleTap   *Action `json:"doubleTap,omitempty"`
	TapHold     *Action `json:"tapHold,omitempty"`
	TappingTerm int     `json:"tappingTerm,omitempty"`
	CustomLabel string  `json:"customLabel,omitempty"`
	Icon        string  `json:"icon,omitempty"`
	Emoji       string  `json:"emoji,omitempty"`
	GlowColor   string  `json:"glowColor,omitempty"`
}

// Action is one binding slot. Layer is a pointer because 0 is a real layer
// (only layer-taking codes like MO/LT/TO set it). Macro is kept raw: its
// shape is undocumented and nothing consumes it yet.
type Action struct {
	Code        string          `json:"code"`
	Layer       *int            `json:"layer,omitempty"`
	Color       string          `json:"color,omitempty"`
	Modifier    string          `json:"modifier,omitempty"`
	Modifiers   *Modifiers      `json:"modifiers,omitempty"`
	Macro       json.RawMessage `json:"macro,omitempty"`
	Description string          `json:"description,omitempty"`
}

// Modifiers is the mod-mask Oryx attaches to modded keys and mod-taps.
type Modifiers struct {
	LeftAlt    bool `json:"leftAlt"`
	LeftCtrl   bool `json:"leftCtrl"`
	LeftShift  bool `json:"leftShift"`
	LeftGui    bool `json:"leftGui"`
	RightAlt   bool `json:"rightAlt"`
	RightCtrl  bool `json:"rightCtrl"`
	RightShift bool `json:"rightShift"`
	RightGui   bool `json:"rightGui"`
}

// Combo is a chord: KeyIndices are positions in the same key order as
// Layer.Keys, Trigger mirrors the Action shape of ordinary keys.
type Combo struct {
	KeyIndices []int   `json:"keyIndices"`
	LayerIdx   int     `json:"layerIdx"`
	Trigger    *Action `json:"trigger,omitempty"`
}

// FetchLayout pulls one Moonlander revision of hashID from Oryx and writes
// it through to the disk cache (keyed by layout: the cache is the current
// board snapshot). revisionID is an exact hash or RevisionLatest; the
// returned Layout.RevisionID names what it resolved to. On a cache-write
// failure the fetched layout is still returned, alongside the error.
func FetchLayout(ctx context.Context, hashID, revisionID string) (*Layout, error) {
	l, err := fetchLayout(ctx, http.DefaultClient, DefaultEndpoint, hashID, revisionID)
	if err != nil {
		return nil, err
	}
	dir, err := cacheDir()
	if err != nil {
		return l, err
	}
	if err := writeCache(dir, hashID, l); err != nil {
		return l, err
	}
	return l, nil
}

// FetchLayoutMeta fetches a revision without touching the disk cache:
// update polling asks for latest, and caching an undeployed revision would
// clobber the snapshot the board loader renders from.
func FetchLayoutMeta(ctx context.Context, hashID, revisionID string) (*Layout, error) {
	return fetchLayout(ctx, http.DefaultClient, DefaultEndpoint, hashID, revisionID)
}

func fetchLayout(ctx context.Context, hc *http.Client, endpoint, hashID, revisionID string) (*Layout, error) {
	if revisionID == "" {
		return nil, errors.New("oryx: empty revision id")
	}
	body, err := json.Marshal(map[string]any{
		"query": getLayoutQuery,
		"variables": map[string]string{
			"hashId":     hashID,
			"geometry":   geometry,
			"revisionId": revisionID,
		},
	})
	if err != nil {
		return nil, fmt.Errorf("oryx: encode request: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("oryx: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := hc.Do(req)
	if err != nil {
		return nil, fmt.Errorf("oryx: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("oryx: %s", resp.Status)
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes+1))
	if err != nil {
		return nil, fmt.Errorf("oryx: read response: %w", err)
	}
	if len(raw) > maxResponseBytes {
		// reject, never truncate into a misleading decode error
		return nil, errors.New("oryx: response exceeds the size cap")
	}
	return decodeResponse(raw)
}

type gqlResponse struct {
	Data struct {
		Layout *layoutWire `json:"layout"`
	} `json:"data"`
	Errors []struct {
		Message string `json:"message"`
	} `json:"errors"`
}

type layoutWire struct {
	HashID   string `json:"hashId"`
	Title    string `json:"title"`
	Geometry string `json:"geometry"`
	Revision struct {
		HashID     string          `json:"hashId"`
		QmkVersion string          `json:"qmkVersion"`
		Title      string          `json:"title"`
		CreatedAt  string          `json:"createdAt"`
		Model      string          `json:"model"`
		MD5        string          `json:"md5"`
		Layers     []Layer         `json:"layers"`
		Combos     []Combo         `json:"combos"`
		Config     json.RawMessage `json:"config"`
	} `json:"revision"`
}

func decodeResponse(raw []byte) (*Layout, error) {
	var resp gqlResponse
	if err := json.Unmarshal(raw, &resp); err != nil {
		return nil, fmt.Errorf("oryx: decode response: %w", err)
	}
	if len(resp.Errors) > 0 {
		return nil, fmt.Errorf("oryx graphql: %s", resp.Errors[0].Message)
	}
	w := resp.Data.Layout
	if w == nil {
		return nil, errors.New("oryx: layout not found")
	}
	return &Layout{
		HashID:     w.HashID,
		Title:      w.Title,
		Geometry:   w.Geometry,
		RevisionID: w.Revision.HashID,
		QmkVersion: w.Revision.QmkVersion,
		CreatedAt:  w.Revision.CreatedAt,
		Model:      w.Revision.Model,
		MD5:        w.Revision.MD5,
		Layers:     w.Revision.Layers,
		Combos:     w.Revision.Combos,
		Config:     w.Revision.Config,
	}, nil
}
