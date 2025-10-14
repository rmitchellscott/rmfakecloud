//go:build !nolibrm

package rmlines

/*
#cgo CPPFLAGS: -I${SRCDIR}/librm_lines/include/headers
#cgo CXXFLAGS: -std=c++20
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
