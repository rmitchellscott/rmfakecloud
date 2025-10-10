package rmlines

/*
#cgo CPPFLAGS: -I${SRCDIR}/librm_lines/rm_lines/headers
#cgo CXXFLAGS: -std=c++20
#cgo LDFLAGS: -L${SRCDIR}/librm_lines/build -lrm_lines -lstdc++ -lm
#include <stdbool.h>
#include <stdlib.h>

const char *buildTree(const char *rmPath);
int destroyTree(const char *treeId);
bool convertToJsonFile(const char *treeId, const char *outPath);
const char *convertToJson(const char *treeId);
const char *getSceneInfo(const char *treeId);
*/
import "C"
import (
	"encoding/json"
	"fmt"
	"math"
	"unsafe"
)

// ParseRM parses a .rm file and returns structured data with CRDT IDs
func ParseRM(rmFilePath string) (*ParsedRM, error) {
	cPath := C.CString(rmFilePath)
	defer C.free(unsafe.Pointer(cPath))

	treeId := C.buildTree(cPath)
	if treeId == nil {
		return nil, fmt.Errorf("failed to build tree from %s", rmFilePath)
	}
	defer C.destroyTree(treeId)

	jsonCStr := C.convertToJson(treeId)
	if jsonCStr == nil {
		return nil, fmt.Errorf("failed to convert tree to JSON")
	}

	jsonStr := C.GoString(jsonCStr)

	var result ParsedRM
	if err := json.Unmarshal([]byte(jsonStr), &result); err != nil {
		return nil, fmt.Errorf("json parse failed: %w", err)
	}

	return &result, nil
}

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
