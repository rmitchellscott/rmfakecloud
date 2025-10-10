package search

type SettingsResponse struct {
	Language      string `json:"language"`
	SearchEnabled bool   `json:"searchEnabled"`
}

type DeltaResponse struct {
	Version    int            `json:"version"`
	Generation int64          `json:"generation"`
	Latest     bool           `json:"latest"`
	Changed    []PageChange   `json:"changed"`
}

type PageChange struct {
	Generation int64  `json:"generation"`
	DocumentID string `json:"documentId,omitempty"`
	PageID     string `json:"pageId"`
	Deleted    bool   `json:"deleted,omitempty"`
}

type SearchIndexResponse struct {
	Words []RecognizedWord `json:"words"`
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
