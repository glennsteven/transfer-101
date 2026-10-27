package transfer

import (
	"context"
	"database/sql"
	"errors"
	"net/url"

	"github.com/lib/pq"
)

func OpenOperatorDatabase(ctx context.Context, dsn string) (*sql.DB, error) {
	u, err := url.Parse(dsn)
	if err != nil || u == nil || (u.Scheme != "postgres" && u.Scheme != "postgresql") || u.Hostname() == "" || u.User == nil || u.User.Username() == "" || u.Path == "" || u.Path == "/" || u.Fragment != "" {
		return nil, errors.New("DATABASE_URL must be an explicit PostgreSQL TCP URL")
	}
	q, err := url.ParseQuery(u.RawQuery)
	if err != nil {
		return nil, errors.New("invalid database options")
	}
	q.Set("connect_timeout", "3")
	u.RawQuery = q.Encode()
	connector, err := pq.NewConnector(u.String())
	if err != nil {
		return nil, errors.New("invalid database options")
	}
	db := sql.OpenDB(connector)
	db.SetMaxOpenConns(2)
	db.SetMaxIdleConns(1)
	if err = CheckSchema(ctx, db); err != nil {
		db.Close()
		return nil, errors.New("operator database/schema unavailable; apply migrations first")
	}
	return db, nil
}
