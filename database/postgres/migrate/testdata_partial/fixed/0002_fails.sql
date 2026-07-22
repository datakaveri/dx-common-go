-- +goose Up
CREATE TABLE IF NOT EXISTS migrate_partial_test_2 (id int);
INSERT INTO migrate_partial_test_2 VALUES (1);

-- +goose Down
DROP TABLE IF EXISTS migrate_partial_test_2;
