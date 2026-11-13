//go:build !windows

package storage

import "context"

func healLocalMySQLPlatform(_ context.Context) MySQLServiceStatus {
	return MySQLServiceStatus{}
}
