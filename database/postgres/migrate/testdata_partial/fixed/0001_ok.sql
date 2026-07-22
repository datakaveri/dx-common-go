-- +goose Up
CREATE TABLE IF NOT EXISTS migrate_partial_test_1 (id int);

-- +goose Down
DROP TABLE IF EXISTS migrate_partial_test_1;
