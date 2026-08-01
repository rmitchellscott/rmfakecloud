package rmlines

import (
	"encoding/json"
	"math"
)

// GetStrokes extracts stroke data with CRDT IDs and bounding boxes
func GetStrokes(parsed *ParsedRM) []Stroke {
	var strokes []Stroke

	for _, node := range parsed.Nodes {
		for _, child := range node.Children {
			if child.Type == "Line" && len(child.Value) > 0 {
				var lineValue LineValue
				if err := json.Unmarshal(child.Value, &lineValue); err != nil {
					continue
				}
				if len(lineValue.Points) == 0 {
					continue
				}

				stroke := Stroke{
					CRDTID: child.ItemID,
					Points: lineValue.Points,
				}

				stroke.BBox = calculateBBox(lineValue.Points)
				strokes = append(strokes, stroke)
			}
		}
	}

	return strokes
}

func calculateBBox(points []Point) BBox {
	if len(points) == 0 {
		return BBox{}
	}

	minX, maxX := points[0].X, points[0].X
	minY, maxY := points[0].Y, points[0].Y

	for _, p := range points[1:] {
		minX = math.Min(minX, p.X)
		maxX = math.Max(maxX, p.X)
		minY = math.Min(minY, p.Y)
		maxY = math.Max(maxY, p.Y)
	}

	return BBox{
		X:      minX,
		Y:      minY,
		Width:  maxX - minX,
		Height: maxY - minY,
	}
}
