package search

import "time"

type SettingsResponse struct {
	Language      string `json:"language"`
	SearchEnabled bool   `json:"searchEnabled"`
}

type DeltaResponse struct {
	Version    int          `json:"version"`
	Generation int64        `json:"generation"`
	Latest     bool         `json:"latest"`
	Changed    []PageChange `json:"changed"`
}

type PageChange struct {
	DeltaID    string `json:"deltaId"`
	Generation int64  `json:"generation"`
	DocumentID string `json:"documentId,omitempty"`
	PageID     string `json:"pageId"`
	Deleted    bool   `json:"deleted,omitempty"`
}

type DeltaAckRequest struct {
	DeltaID    string `json:"deltaId"`
	DocumentID string `json:"documentId,omitempty"`
	PageID     string `json:"pageId,omitempty"`
	Latest     bool   `json:"latest,omitempty"`
}

type DeviceSearchState struct {
	UserID           string    `json:"userId"`
	DeviceID         string    `json:"deviceId"`
	LastAckedDeltaID string    `json:"lastAckedDeltaId"`
	LastGeneration   int64     `json:"lastGeneration"`
	UpdatedAt        time.Time `json:"updatedAt"`
}

type SearchIndexResponse struct {
	Version     int             `json:"version"`
	Generation  int64           `json:"generation"`
	Handwritten HandwrittenData `json:"handwritten"`
}

type HandwrittenData struct {
	Content     string      `json:"content"`
	MainStrokes MainStrokes `json:"mainStrokes"`
}

type MainStrokes struct {
	Resolution string     `json:"resolution"`
	Strokes    [][]string `json:"strokes"`
}

type RecognizedWord struct {
	Text    string        `json:"text"`
	BBox    BoundingBox   `json:"bbox"`
	Strokes []string      `json:"strokes"`
}

type BoundingBox struct {
	X      float64 `json:"x"`
	Y      float64 `json:"y"`
	Width  float64 `json:"width"`
	Height float64 `json:"height"`
}

type CachedIndex struct {
	Generation int64                `json:"generation"`
	Response   SearchIndexResponse  `json:"response"`
}
