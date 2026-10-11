// Package transferlimit limits serialized transfer data, not database/network
// observation memory. Collectors account fragments as they append them; one
// fragment and the existing store observation can already reside in memory.
package transferlimit

import (
	"encoding/json"
	"errors"
	"fmt"
)

const DefaultBytes int64 = 64 << 20

var ErrCapacity = errors.New("store-transfer input exceeds byte limit")

func Bytes(value int64) (int64, error) {
	if value == 0 {
		return DefaultBytes, nil
	}
	if value < 0 || value > 1<<30 {
		return 0, errors.New("transfer byte limit must be between 1 and 1073741824")
	}
	return value, nil
}

type Budget struct{ remaining int64 }

func New(value int64) (*Budget, error) {
	limit, err := Bytes(value)
	if err != nil {
		return nil, err
	}
	return &Budget{remaining: limit}, nil
}

// Add conservatively counts each JSON fragment including its newline. The
// source header and fragments are charged once; callers also check the final
// envelope exactly. Never expose a partial source after a budget failure.
func (b *Budget) Add(value any) error {
	if err := json.NewEncoder(b).Encode(value); err != nil {
		return fmt.Errorf("collect transfer data: %w", err)
	}
	return nil
}

func (b *Budget) Write(data []byte) (int, error) {
	if int64(len(data)) > b.remaining {
		return 0, ErrCapacity
	}
	b.remaining -= int64(len(data))
	return len(data), nil
}
