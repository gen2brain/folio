package html

import (
	"fmt"
	"io"
)

// openMOBI is not written yet.
func openMOBI(r io.ReaderAt, size int64) (*Document, error) {
	return nil, fmt.Errorf("%w: MOBI", ErrUnsupported)
}
