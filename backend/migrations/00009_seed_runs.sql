-- +goose Up

-- Simülatörün toplu seed'inin kaydı: aynı tohumla yapılmış seed bir daha çalıştırılmaz (PLAN.md §6).
CREATE TABLE seed_runs (
    name         text        PRIMARY KEY,
    seed         bigint      NOT NULL,
    customers    int         NOT NULL,
    accounts     int         NOT NULL,
    completed_at timestamptz NOT NULL DEFAULT now()
);

-- +goose Down
DROP TABLE seed_runs;
