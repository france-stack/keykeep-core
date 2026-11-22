package virtualkey

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/keykeep/gateway/internal/dbpool"
)

// PostgresStore 直接读控制面的 ApiKey 表；撤销即 revokedAt 非空。
type PostgresStore struct {
	pool *pgxpool.Pool
}

func NewPostgresStore(ctx context.Context, databaseURL string) (*PostgresStore, error) {
	pool, err := dbpool.Open(ctx, databaseURL)
	if err != nil {
		return nil, err
	}
	return &PostgresStore{pool: pool}, nil
}

// Ping 供就绪探针使用：数据库可达才算网关就绪（Key 校验与计量都依赖它）。
func (s *PostgresStore) Ping(ctx context.Context) error {
	if s == nil || s.pool == nil {
		return errors.New("virtualkey: 存储未初始化")
	}
	return s.pool.Ping(ctx)
}

func (s *PostgresStore) Lookup(ctx context.Context, keyHash []byte) (*KeyInfo, error) {
	var (
		id      string
		emp     string
		dept    *string
		prefix4 string
		fp6     string
		allowed []string
	)
	err := s.pool.QueryRow(ctx,
		`SELECT k.id::text, k."empId"::text, u."deptId"::text, k."prefix4", k.fp6, k."allowedModels"
		   FROM "ApiKey" k
		   JOIN "User" u ON u.id = k."empId"
		  WHERE k."keyHash" = $1 AND k."revokedAt" IS NULL AND u.disabled = false`,
		keyHash,
	).Scan(&id, &emp, &dept, &prefix4, &fp6, &allowed)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &KeyInfo{ID: id, EmpID: emp, DeptID: dept, Prefix4: prefix4, FP6: fp6, AllowedModels: allowed}, nil
}

func (s *PostgresStore) Close() {
	s.pool.Close()
}
