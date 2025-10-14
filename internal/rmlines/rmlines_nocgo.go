//go:build nolibrm

package rmlines

import "fmt"

// ParseRM is a stub that returns an error when librm_lines is not available
func ParseRM(rmFilePath string) (*ParsedRM, error) {
	return nil, fmt.Errorf("rmlines: librm_lines not available (built with nolibrm tag)")
}
