package postgres

import (
	"context"

	"github.com/jackc/pgx/v5"
	"github.com/melmut-42/ft_transcendence/backend/user-service/internal/service"
)

var _ service.AvatarReferences = (*AvatarRepository)(nil)

// ReferencedUploads implements service.AvatarReferences.
func (r *AvatarRepository) ReferencedUploads(ctx context.Context, keys []string) (map[string]bool, error) {
	rows, err := r.pool.Query(ctx, `SELECT avatar_ref FROM profiles
		WHERE avatar_kind = 'upload' AND avatar_ref = ANY($1)`, keys)
	if err != nil {
		return nil, classify(err)
	}
	refs, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return nil, classify(err)
	}
	referenced := make(map[string]bool, len(refs))
	for _, ref := range refs {
		referenced[ref] = true
	}

	return referenced, nil
}
