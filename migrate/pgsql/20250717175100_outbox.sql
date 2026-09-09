-- +goose Up
create table if not exists transactional_outbox (
    id uuid primary key,
    aggregate_id integer not null,
    aggregate_type varchar(100) not null,
    topic varchar(255) not null,
    payload text null,
    created_at timestamp default current_timestamp,
    processed_at timestamp null,
    next_attempt_at timestamp null,
    status varchar(20) not null check (
        status in ('pending', 'processed', 'failed')
    ),
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
