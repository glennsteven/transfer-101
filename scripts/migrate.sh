#!/bin/sh
exec dbmate --env DATABASE_URL --migrations-dir /app/internal/transfer/migrations \
    --migrations-table dbmate_schema_migrations --no-dump-schema migrate
