package rmlines

import "encoding/json"

type ParsedRM struct {
	Nodes map[string]Node `json:"nodes"`
}

type Node struct {
	Children []Child `json:"children"`
}

type Child struct {
	Type    string          `json:"_type"`
	ItemID  string          `json:"itemId"`
	LeftID  string          `json:"leftId"`
	RightID string          `json:"rightId"`
	Value   json.RawMessage `json:"value,omitempty"`
}

type LineValue struct {
	Points []Point `json:"points"`
	Tool   int     `json:"tool,omitempty"`
	Color  int     `json:"color,omitempty"`
}

type Point struct {
	X        float64 `json:"x"`
	Y        float64 `json:"y"`
	Pressure int     `json:"pressure,omitempty"`
	Speed    int     `json:"speed,omitempty"`
}

type Stroke struct {
	CRDTID string
	Points []Point
	BBox   BBox
}

type BBox struct {
	X      float64
	Y      float64
	Width  float64
	Height float64
}
