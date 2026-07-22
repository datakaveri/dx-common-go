-- +goose Up
CREATE TABLE IF NOT EXISTS migrate_partial_test_2 (id int);
INSERT INTO no_such_table_xyz VALUES (1);

-- +goose Down
DROP TABLE IF EXISTS migrate_partial_test_2;
