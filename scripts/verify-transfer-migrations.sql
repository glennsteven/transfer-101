BEGIN READ ONLY;

SELECT current_database() AS database, current_schema() AS schema,
       current_setting('server_version') AS server_version,
       to_regclass('dbmate_schema_migrations') AS history_table;

WITH expected(version) AS (
    VALUES ('20261020000100'), ('20261020000300'),
           ('20261021000100'), ('20261021000300')
)
SELECT expected.version, history.version IS NOT NULL AS applied
FROM expected
LEFT JOIN dbmate_schema_migrations AS history USING (version)
ORDER BY expected.version;

ROLLBACK;
