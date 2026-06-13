package db

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"
)

// enumTypes are custom Postgres types that must be registered with pgx so that
// values (and arrays of them) decode correctly. Array types are auto-derived
// by LoadType when the element type is loaded first.
var enumTypes = []string{"profile_role", "_profile_role"}

// registerEnumTypes loads custom enum OIDs and registers them on the connection.
func registerEnumTypes(ctx context.Context, conn *pgx.Conn) error {
	for _, name := range enumTypes {
		t, err := conn.LoadType(ctx, name)
		if err != nil {
			return fmt.Errorf("load pg type %q: %w", name, err)
		}
		conn.TypeMap().RegisterType(t)
	}
	return nil
}
