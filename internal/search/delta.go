package search

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	log "github.com/sirupsen/logrus"
)

const (
	// PageSize is the number of changes to return per delta request
	// This matches the reMarkable cloud's pagination behavior (~100 items)
	PageSize = 100
)

type DeltaTracker struct {
	dataDir string
	mu      sync.RWMutex
}

func NewDeltaTracker(dataDir string) (*DeltaTracker, error) {
	return &DeltaTracker{
		dataDir: dataDir,
	}, nil
}

func generateDeltaID() string {
	b := make([]byte, 16)
	rand.Read(b)
	return hex.EncodeToString(b)
}

func (dt *DeltaTracker) getPagesFromCache(uid string, since int64) ([]PageChange, error) {
	cacheDir := filepath.Join(dt.dataDir, "users", uid, "search", "cache")

	if _, err := os.Stat(cacheDir); os.IsNotExist(err) {
		return []PageChange{}, nil
	}

	var pages []PageChange

	err := filepath.Walk(cacheDir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}

		if info.IsDir() || !strings.HasSuffix(path, ".json") {
			return nil
		}

		data, err := os.ReadFile(path)
		if err != nil {
			log.Warnf("Failed to read cache file %s: %v", path, err)
			return nil
		}

		var cached CachedIndex
		if err := json.Unmarshal(data, &cached); err != nil {
			log.Warnf("Failed to parse cache file %s: %v", path, err)
			return nil
		}

		if len(cached.Response.Handwritten.MainStrokes.Strokes) == 0 {
			return nil
		}

		if cached.Generation <= since {
			return nil
		}

		relPath, err := filepath.Rel(cacheDir, path)
		if err != nil {
			log.Warnf("Failed to get relative path for %s: %v", path, err)
			return nil
		}

		parts := strings.Split(relPath, string(os.PathSeparator))
		if len(parts) != 2 {
			log.Warnf("Unexpected cache path format: %s", relPath)
			return nil
		}

		docID := parts[0]
		pageID := strings.TrimSuffix(parts[1], ".json")

		pages = append(pages, PageChange{
			DeltaID:    generateDeltaID(),
			Generation: cached.Generation,
			DocumentID: docID,
			PageID:     pageID,
			Deleted:    false,
		})

		return nil
	})

	if err != nil {
		return nil, err
	}

	return pages, nil
}

func (dt *DeltaTracker) GetDelta(uid string, since int64) (*DeltaResponse, error) {
	dt.mu.RLock()
	defer dt.mu.RUnlock()

	pages, err := dt.getPagesFromCache(uid, since)
	if err != nil {
		return nil, err
	}

	if len(pages) == 0 {
		return &DeltaResponse{
			Version:    1,
			Generation: since,
			Latest:     true,
			Changed:    []PageChange{},
		}, nil
	}

	sort.Slice(pages, func(i, j int) bool {
		return pages[i].Generation < pages[j].Generation
	})

	if len(pages) <= PageSize {
		responseGeneration := pages[len(pages)-1].Generation
		log.Debugf("Delta sync (since=%d): returning %d pages, latest=true, generation=%d",
			since, len(pages), responseGeneration)

		return &DeltaResponse{
			Version:    1,
			Generation: responseGeneration,
			Latest:     true,
			Changed:    pages,
		}, nil
	}

	responseGeneration := pages[PageSize-1].Generation
	log.Debugf("Delta sync (since=%d): returning %d/%d pages, latest=false, generation=%d",
		since, PageSize, len(pages), responseGeneration)

	return &DeltaResponse{
		Version:    1,
		Generation: responseGeneration,
		Latest:     false,
		Changed:    pages[:PageSize],
	}, nil
}
