-- +goose Up
create table if not exists transactional_outbox (
    id text primary key,
    aggregate_id integer not null,
    aggregate_type text not null,
    topic text not null,
    payload text null,
    created_at datetime default current_timestamp,
    processed_at datetime null,
    next_attempt_at datetime null,
    status text not null check (status in ('pending', 'processed', 'failed')),
    retry_count integer not null,
    max_retries integer not null,
    last_error text not null,
    metadata text null
);

create index if not exists transactional_outbox_publisher on transactional_outbox (
    status
);

create index if not exists transactional_outbox_cleanup on transactional_outbox (
    status, processed_at, id
);

create index if not exists transactional_outbox_retry_schedule on transactional_outbox (
    status, created_at, id, next_attempt_at
);

-- +goose Down
drop table if exists transactional_outbox;
