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

	"github.com/ddvk/rmfakecloud/internal/storage/models"
	log "github.com/sirupsen/logrus"
)

const (
	// PageSize is the number of changes to return per delta request
	// This matches the reMarkable cloud's pagination behavior (~100 items)
	PageSize = 100

	// xochitl compares this with an exact match and rejects any other value.
	deltaProtocolVersion = 2
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

type treeIndex struct {
	syncDir string
	docs    map[string]string
	pages   map[string]map[string]bool
}

func (dt *DeltaTracker) loadTreeIndex(uid string) (*treeIndex, error) {
	syncDir := filepath.Join(dt.dataDir, "users", uid, "sync")

	rootData, err := os.ReadFile(filepath.Join(syncDir, "root"))
	if err != nil {
		return nil, err
	}

	rootIndex, err := os.Open(filepath.Join(syncDir, strings.TrimSpace(string(rootData))))
	if err != nil {
		return nil, err
	}
	defer rootIndex.Close()

	entries, err := models.ParseIndex(rootIndex)
	if err != nil {
		return nil, err
	}

	docs := make(map[string]string, len(entries))
	for _, entry := range entries {
		docs[entry.EntryName] = entry.Hash
	}

	return &treeIndex{
		syncDir: syncDir,
		docs:    docs,
		pages:   make(map[string]map[string]bool),
	}, nil
}

// hasPage reports whether the page still has a .rm blob in the tree.
func (t *treeIndex) hasPage(docID, pageID string) bool {
	docHash, ok := t.docs[docID]
	if !ok {
		return false
	}

	pages, parsed := t.pages[docID]
	if !parsed {
		pages = make(map[string]bool)

		docIndex, err := os.Open(filepath.Join(t.syncDir, docHash))
		if err != nil {
			log.Warnf("Cannot open document index %s: %v", docID, err)
			t.pages[docID] = nil
			return true
		}
		defer docIndex.Close()

		entries, err := models.ParseIndex(docIndex)
		if err != nil {
			log.Warnf("Cannot parse document index %s: %v", docID, err)
			t.pages[docID] = nil
			return true
		}

		for _, entry := range entries {
			if strings.HasSuffix(entry.EntryName, ".rm") {
				pages[strings.TrimSuffix(filepath.Base(entry.EntryName), ".rm")] = true
			}
		}
		t.pages[docID] = pages
	}

	if pages == nil {
		return true
	}
	return pages[pageID]
}

func (dt *DeltaTracker) getPagesFromCache(uid string, since int64) ([]PageChange, error) {
	cacheDir := filepath.Join(dt.dataDir, "users", uid, "search", "cache")

	tree, treeErr := dt.loadTreeIndex(uid)
	if treeErr != nil {
		log.Warnf("Cannot resolve tree for %s, reporting all cached pages as live: %v", uid, treeErr)
	}

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

		deleted := tree != nil && !tree.hasPage(docID, pageID)
		if deleted {
			log.Debugf("Reporting %s/%s as deleted, no .rm blob in the tree", docID, pageID)
		}

		pages = append(pages, PageChange{
			DeltaID:    generateDeltaID(),
			Generation: cached.Generation,
			DocumentID: docID,
			PageID:     pageID,
			Deleted:    deleted,
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
			Version:    deltaProtocolVersion,
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
			Version:    deltaProtocolVersion,
			Generation: responseGeneration,
			Latest:     true,
			Changed:    pages,
		}, nil
	}

	responseGeneration := pages[PageSize-1].Generation
	log.Debugf("Delta sync (since=%d): returning %d/%d pages, latest=false, generation=%d",
		since, PageSize, len(pages), responseGeneration)

	return &DeltaResponse{
		Version:    deltaProtocolVersion,
		Generation: responseGeneration,
		Latest:     false,
		Changed:    pages[:PageSize],
	}, nil
}
