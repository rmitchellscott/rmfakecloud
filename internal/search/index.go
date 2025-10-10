package search

import (
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/ddvk/rmfakecloud/internal/hwr"
	"github.com/ddvk/rmfakecloud/internal/rmlines"
	log "github.com/sirupsen/logrus"
)

// MyScript returns coordinates in millimeters. We multiply by DPI/25.4 to convert to pixels.
// However, since we send coordinates ×10 to MyScript, we need to divide by 10 at the end.
// So: (DPI / 25.4) / 10 = 2280 / 25.4 / 10 = 8.976
const SCALE_FACTOR = 8.976

type MyScriptRequest struct {
	ContentType   string           `json:"contentType"`
	Width         int              `json:"width"`
	Height        int              `json:"height"`
	XDPI          int              `json:"xDPI"`
	YDPI          int              `json:"yDPI"`
	StrokeGroups  []StrokeGroup    `json:"strokeGroups"`
	Configuration MSConfiguration  `json:"configuration"`
}

type StrokeGroup struct {
	Strokes []MSStroke `json:"strokes"`
}

type MSStroke struct {
	X []float64 `json:"x"`
	Y []float64 `json:"y"`
}

type MSConfiguration struct {
	Lang       string           `json:"lang"`
	Export     ExportConfig     `json:"export"`
	RawContent RawContentConfig `json:"raw-content"`
}

type ExportConfig struct {
	JIIX JIIXConfig `json:"jiix"`
}

type JIIXConfig struct {
	Strokes bool       `json:"strokes"`
	Text    TextExport `json:"text"`
}

type TextExport struct {
	Chars bool `json:"chars"`
	Words bool `json:"words"`
}

type RawContentConfig struct {
	Recognition RecognitionConfig `json:"recognition"`
}

type RecognitionConfig struct {
	Shape bool `json:"shape"`
	Text  bool `json:"text"`
}

type MyScriptResponse struct {
	Elements []MSElement `json:"elements"`
}

type MSElement struct {
	Words []MSWord `json:"words"`
}

type MSWord struct {
	Label       string      `json:"label"`
	BoundingBox MSBBox      `json:"bounding-box"`
}

type MSBBox struct {
	X      float64 `json:"x"`
	Y      float64 `json:"y"`
	Width  float64 `json:"width"`
	Height float64 `json:"height"`
}

type StrokeMapping struct {
	Index  int
	CRDTID string
	Layer  string
}

type IndexManager struct {
	dataDir   string
	hwrClient *hwr.HWRClient
}

func NewIndexManager(dataDir string, hwrClient *hwr.HWRClient) *IndexManager {
	return &IndexManager{
		dataDir:   dataDir,
		hwrClient: hwrClient,
	}
}

func (im *IndexManager) cacheFilePath(uid, docID, pageID string) string {
	return filepath.Join(im.dataDir, "users", uid, "search", "cache", docID, fmt.Sprintf("%s.json", pageID))
}

func (im *IndexManager) GetOrBuildIndex(uid, docID, pageID string, rmFilePath string, generation int64) (*SearchIndexResponse, error) {
	cachePath := im.cacheFilePath(uid, docID, pageID)

	if cached, err := im.loadCachedIndex(cachePath); err == nil {
		if cached.Generation == generation {
			log.Debugf("Using cached search index for %s/%s (gen=%d)", docID, pageID, generation)
			return &cached.Response, nil
		}
		log.Debugf("Cache outdated for %s/%s (cache=%d, current=%d)", docID, pageID, cached.Generation, generation)
	}

	log.Infof("Building search index for %s/%s", docID, pageID)
	index, err := im.buildIndex(rmFilePath)
	if err != nil {
		return nil, err
	}

	if err := im.saveCachedIndex(cachePath, generation, index); err != nil {
		log.Warnf("Failed to cache search index: %v", err)
	}

	return index, nil
}

func (im *IndexManager) loadCachedIndex(path string) (*CachedIndex, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}

	var cached CachedIndex
	if err := json.Unmarshal(data, &cached); err != nil {
		return nil, err
	}

	return &cached, nil
}

func (im *IndexManager) saveCachedIndex(path string, generation int64, response *SearchIndexResponse) error {
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}

	cached := CachedIndex{
		Generation: generation,
		Response:   *response,
	}

	data, err := json.MarshalIndent(cached, "", "  ")
	if err != nil {
		return err
	}

	return os.WriteFile(path, data, 0600)
}

func (im *IndexManager) buildIndex(rmFilePath string) (*SearchIndexResponse, error) {
	parsed, err := rmlines.ParseRM(rmFilePath)
	if err != nil {
		return nil, fmt.Errorf("failed to parse .rm file: %w", err)
	}

	msRequest, mappings, err := convertToMyScriptFormat(parsed, "en_US")
	if err != nil {
		return nil, fmt.Errorf("failed to convert to MyScript format: %w", err)
	}
	_ = mappings

	msRequestJSON, err := json.Marshal(msRequest)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal MyScript request: %w", err)
	}

	// If no strokes to recognize, return empty index immediately
	if len(msRequest.StrokeGroups) == 0 {
		log.Infof("No strokes to recognize, returning empty index")
		return &SearchIndexResponse{
			Handwritten: HandwrittenData{
				Content: "",
				MainStrokes: MainStrokes{
					Resolution: "WORD",
					Strokes:    [][]string{},
				},
			},
		}, nil
	}

	// Debug: log first stroke we're sending to MyScript
	if len(msRequest.StrokeGroups) > 0 && len(msRequest.StrokeGroups[0].Strokes) > 0 {
		firstStroke := msRequest.StrokeGroups[0].Strokes[0]
		if len(firstStroke.X) > 0 && len(firstStroke.Y) > 0 {
			log.Debugf("First stroke sent to MyScript (×10): X[0]=%.2f Y[0]=%.2f (count=%d points)",
				firstStroke.X[0], firstStroke.Y[0], len(firstStroke.X))
		}
	}

	jiixBytes, err := im.hwrClient.SendRequest(msRequestJSON)
	if err != nil {
		return nil, fmt.Errorf("MyScript API call failed: %w", err)
	}

	var jiixResponse MyScriptResponse
	if err := json.Unmarshal(jiixBytes, &jiixResponse); err != nil {
		return nil, fmt.Errorf("failed to parse MyScript response: %w", err)
	}

	// Debug: log first word MyScript returned (BEFORE scaling)
	if len(jiixResponse.Elements) > 0 && len(jiixResponse.Elements[0].Words) > 0 {
		firstWord := jiixResponse.Elements[0].Words[0]
		log.Debugf("First word from MyScript (BEFORE x89 scaling): '%s' X=%.2f Y=%.2f W=%.2f H=%.2f",
			firstWord.Label, firstWord.BoundingBox.X, firstWord.BoundingBox.Y,
			firstWord.BoundingBox.Width, firstWord.BoundingBox.Height)
	}

	strokes := rmlines.GetStrokes(parsed)
	log.Debugf("GetStrokes returned %d strokes", len(strokes))

	var contentParts []string
	var strokesArray [][]string

	for _, element := range jiixResponse.Elements {
		for _, word := range element.Words {
			if strings.TrimSpace(word.Label) == "" {
				continue
			}

			scaledBBox := BoundingBox{
				X:      word.BoundingBox.X * SCALE_FACTOR,
				Y:      word.BoundingBox.Y * SCALE_FACTOR,
				Width:  word.BoundingBox.Width * SCALE_FACTOR,
				Height: word.BoundingBox.Height * SCALE_FACTOR,
			}

			matchedCRDTIDs := mapWordToStrokes(scaledBBox, strokes)
			strokeRefs := formatStrokeReferences(matchedCRDTIDs)

			contentParts = append(contentParts, word.Label)
			strokesArray = append(strokesArray, strokeRefs)
		}
	}

	// Join words with spaces to create content string
	content := strings.Join(contentParts, " ")

	return &SearchIndexResponse{
		Handwritten: HandwrittenData{
			Content: content,
			MainStrokes: MainStrokes{
				Resolution: "WORD",
				Strokes:    strokesArray,
			},
		},
	}, nil
}

func convertToMyScriptFormat(parsed *rmlines.ParsedRM, lang string) (*MyScriptRequest, []StrokeMapping, error) {
	layerStrokes := make(map[string][]rmlines.Stroke)

	log.Debugf("Parsing nodes: found %d nodes", len(parsed.Nodes))

	for _, node := range parsed.Nodes {
		log.Debugf("Node has %d children", len(node.Children))
		for i, child := range node.Children {
			log.Debugf("  Child[%d]: Type=%s, ItemID=%s, ValueLen=%d", i, child.Type, child.ItemID, len(child.Value))
		}

		for _, child := range node.Children {
			if child.Type != "Line" || len(child.Value) == 0 {
				continue
			}

			var lineValue rmlines.LineValue
			if err := json.Unmarshal(child.Value, &lineValue); err != nil {
				continue
			}
			if len(lineValue.Points) == 0 {
				continue
			}

			parts := strings.Split(child.ItemID, ":")
			if len(parts) != 2 {
				continue
			}
			layer := parts[0]

			stroke := rmlines.Stroke{
				CRDTID: child.ItemID,
				Points: lineValue.Points,
			}

			layerStrokes[layer] = append(layerStrokes[layer], stroke)
		}
	}

	totalStrokes := 0
	for _, strokes := range layerStrokes {
		totalStrokes += len(strokes)
	}
	log.Debugf("Built layerStrokes: %d layers with %d total strokes", len(layerStrokes), totalStrokes)

	// If no strokes found, return empty request (don't send to MyScript)
	// This handles typed text pages or empty pages
	if totalStrokes == 0 {
		log.Infof("No handwriting strokes found (possibly typed text page or empty page)")
		return &MyScriptRequest{
			ContentType:  "Raw Content",
			Width:        14040,
			Height:       18720,
			XDPI:         2280,
			YDPI:         2280,
			StrokeGroups: []StrokeGroup{},
			Configuration: MSConfiguration{
				Lang: lang,
				Export: ExportConfig{
					JIIX: JIIXConfig{
						Strokes: false,
						Text: TextExport{
							Chars: false,
							Words: true,
						},
					},
				},
				RawContent: RawContentConfig{
					Recognition: RecognitionConfig{
						Shape: false,
						Text:  true,
					},
				},
			},
		}, []StrokeMapping{}, nil
	}

	var strokeGroups []StrokeGroup
	var mappings []StrokeMapping
	globalIndex := 0

	layers := make([]string, 0, len(layerStrokes))
	for layer := range layerStrokes {
		layers = append(layers, layer)
	}
	sort.Strings(layers)

	for _, layer := range layers {
		strokes := layerStrokes[layer]
		var msStrokes []MSStroke

		for _, stroke := range strokes {
			var xCoords []float64
			var yCoords []float64

			for _, point := range stroke.Points {
				// Multiply by 10 to match device behavior (MyScript expects coordinates × 10)
				xCoords = append(xCoords, point.X*10)
				yCoords = append(yCoords, point.Y*10)
			}

			msStrokes = append(msStrokes, MSStroke{
				X: xCoords,
				Y: yCoords,
			})

			mappings = append(mappings, StrokeMapping{
				Index:  globalIndex,
				CRDTID: stroke.CRDTID,
				Layer:  layer,
			})
			globalIndex++
		}

		strokeGroups = append(strokeGroups, StrokeGroup{
			Strokes: msStrokes,
		})
	}

	request := &MyScriptRequest{
		ContentType:  "Raw Content",
		Width:        14040, // reMarkable page width
		Height:       18720, // reMarkable page height
		XDPI:         2280,  // reMarkable DPI
		YDPI:         2280,  // reMarkable DPI
		StrokeGroups: strokeGroups,
		Configuration: MSConfiguration{
			Lang: lang,
			Export: ExportConfig{
				JIIX: JIIXConfig{
					Strokes: false,
					Text: TextExport{
						Chars: false,
						Words: true,
					},
				},
			},
			RawContent: RawContentConfig{
				Recognition: RecognitionConfig{
					Shape: false,
					Text:  true,
				},
			},
		},
	}

	totalRequestStrokes := 0
	for _, sg := range strokeGroups {
		totalRequestStrokes += len(sg.Strokes)
	}
	log.Debugf("MyScript request: %d strokeGroups with %d total strokes", len(strokeGroups), totalRequestStrokes)

	return request, mappings, nil
}

func mapWordToStrokes(wordBBox BoundingBox, strokes []rmlines.Stroke) []string {
	var matchedCRDTIDs []string

	for _, stroke := range strokes {
		strokeBBox := stroke.BBox

		if bboxOverlap(strokeBBox, wordBBox, 0.1) {
			matchedCRDTIDs = append(matchedCRDTIDs, stroke.CRDTID)
		}
	}

	sort.Strings(matchedCRDTIDs)
	return matchedCRDTIDs
}

func bboxOverlap(strokeBBox rmlines.BBox, wordBBox BoundingBox, threshold float64) bool {
	xLeft := math.Max(strokeBBox.X, wordBBox.X)
	xRight := math.Min(strokeBBox.X+strokeBBox.Width, wordBBox.X+wordBBox.Width)
	yTop := math.Max(strokeBBox.Y, wordBBox.Y)
	yBottom := math.Min(strokeBBox.Y+strokeBBox.Height, wordBBox.Y+wordBBox.Height)

	if xRight < xLeft || yBottom < yTop {
		return false
	}

	strokeArea := strokeBBox.Width * strokeBBox.Height

	if strokeArea == 0 {
		strokeLength := math.Max(strokeBBox.Width, strokeBBox.Height)
		if strokeLength == 0 {
			return true
		}
		intersectionLength := math.Max((xRight - xLeft), (yBottom - yTop))
		return (intersectionLength / strokeLength) > 0.5
	}

	intersectionArea := (xRight - xLeft) * (yBottom - yTop)
	return (intersectionArea / strokeArea) > threshold
}

func formatStrokeReferences(crdtIDs []string) []string {
	if len(crdtIDs) == 0 {
		return []string{}
	}

	type Range struct {
		Layer string
		Start int
		Count int
	}

	var ranges []Range
	var currentRange Range
	currentRangeActive := false

	for _, crdtID := range crdtIDs {
		parts := strings.Split(crdtID, ":")
		if len(parts) != 2 {
			continue
		}
		layer := parts[0]
		id, err := strconv.Atoi(parts[1])
		if err != nil {
			continue
		}

		if !currentRangeActive {
			currentRange = Range{Layer: layer, Start: id, Count: 1}
			currentRangeActive = true
			continue
		}

		if layer == currentRange.Layer && id == currentRange.Start+currentRange.Count {
			currentRange.Count++
		} else {
			ranges = append(ranges, currentRange)
			currentRange = Range{Layer: layer, Start: id, Count: 1}
		}
	}
	if currentRangeActive {
		ranges = append(ranges, currentRange)
	}

	var refs []string
	for _, r := range ranges {
		refs = append(refs, fmt.Sprintf("%s:%d;%d", r.Layer, r.Start, r.Count))
	}

	return refs
}
