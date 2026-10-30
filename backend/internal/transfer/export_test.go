package transfer

import (
	"context"

	"github.com/google/uuid"

	"parakopru/internal/platform/db"
)

// MoveForTest geçiş tablosunu dışarıdan sınamak içindir: transferi kilitleyip move ile verilen duruma geçirmeye çalışır.
func (s *Service) MoveForTest(ctx context.Context, id uuid.UUID, to State) error {
	return s.tx.WithTx(ctx, func(ctx context.Context, tx db.DBTX) error {
		t, err := lock(ctx, tx, id)
		if err != nil {
			return err
		}
		return s.move(ctx, tx, &t, to, nil, nil, "test")
	})
}
