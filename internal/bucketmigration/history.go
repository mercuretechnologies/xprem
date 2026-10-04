package bucketmigration

import (
	"context"
	"io"
	"slices"
	"strings"
	"xprem/internal/objectstore"
)

const historyKey = ".migrationhistory"

// History is the ledger of applied migrations, one id per line at the root
// of the bucket.
type History struct {
	objectStore objectstore.Store
}

func NewHistory(objectStore objectstore.Store) *History {
	return &History{objectStore: objectStore}
}

func (h *History) Applied() ([]string, error) {
	file, err := h.objectStore.Get(context.Background(), historyKey)
	if err != nil || file == nil {
		return nil, err
	}
	defer file.Reader.Close()
	var content strings.Builder
	if _, err := io.Copy(&content, file.Reader); err != nil {
		return nil, err
	}
	var applied []string
	for _, line := range strings.Split(content.String(), "\n") {
		if line = strings.TrimSpace(line); line != "" {
			applied = append(applied, line)
		}
	}
	return applied, nil
}

func (h *History) Record(id string) error {
	applied, err := h.Applied()
	if err != nil {
		return err
	}
	if slices.Contains(applied, id) {
		return nil
	}
	return h.write(append(applied, id))
}

func (h *History) Forget(id string) error {
	applied, err := h.Applied()
	if err != nil {
		return err
	}
	if !slices.Contains(applied, id) {
		return nil
	}
	return h.write(slices.DeleteFunc(applied, func(applied string) bool { return applied == id }))
}

func (h *History) write(ids []string) error {
	var content strings.Builder
	for _, id := range ids {
		content.WriteString(id + "\n")
	}
	return h.objectStore.Put(context.Background(), historyKey, strings.NewReader(content.String()))
}
